package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	claudeAccountProfileFailedAtKey    = "claude_account_profile_failed_at"
	claudeAccountProfileTokenSHA256Key = "claude_account_profile_token_sha256"
	claudeAccountProfileTimeout        = 10 * time.Second
	// claudeAccountProfileNegativeCacheTTL bounds how long a failed profile
	// lookup suppresses repeated attempts. Requests use a stable fallback
	// identity inside the window, and the first request after expiry retries.
	claudeAccountProfileNegativeCacheTTL = time.Hour
	claudeAccountProfileCacheCapacity    = 1024

	// These keys were written by the first negative-cache implementation. They
	// are read only to migrate an existing credential to the compact state.
	claudeAccountProfileLegacyCheckedAtKey = "claude_account_profile_checked_at"
	claudeAccountProfileLegacyFallbackKey  = "claude_account_profile_fallback"
)

type claudeAccountProfileSnapshot struct {
	accountUUID      string
	email            string
	organizationUUID string
	organizationName string
	failedAt         time.Time
	tokenSHA256      string
}

type claudeAccountProfileCacheEntry struct {
	mu       sync.Mutex
	snapshot claudeAccountProfileSnapshot
}

// claudeAccountProfileCache bridges profile state across ephemeral Home auth
// snapshots. Local credentials still persist the same state through Manager.Update.
type claudeAccountProfileCache struct {
	once    sync.Once
	entries *internalcache.BoundedLRU[[sha256.Size]byte, *claudeAccountProfileCacheEntry]
}

func (cache *claudeAccountProfileCache) entry(key [sha256.Size]byte) *claudeAccountProfileCacheEntry {
	cache.once.Do(func() {
		cache.entries = internalcache.NewBoundedLRU[[sha256.Size]byte, *claudeAccountProfileCacheEntry](claudeAccountProfileCacheCapacity, nil)
	})
	return cache.entries.GetOrAdd(key, func() *claudeAccountProfileCacheEntry {
		return &claudeAccountProfileCacheEntry{}
	})
}

func claudeAccountProfileCacheKey(auth *cliproxyauth.Auth, apiKey string) ([sha256.Size]byte, bool) {
	var zero [sha256.Size]byte
	identity := helps.ClaudeCLIAuthIdentitySeed(auth)
	tokenSHA256 := claudeAccountProfileTokenSHA256(apiKey)
	if identity == "" || tokenSHA256 == "" {
		return zero, false
	}
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	// Hash the complete partition key so neither the token nor other credential
	// material is retained as a plaintext map key.
	return sha256.Sum256([]byte(identity + "\x00" + tokenSHA256 + "\x00" + proxyURL)), true
}

func claudeAccountProfileTokenSHA256(apiKey string) string {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(digest[:])
}

func claudeAccountProfileTokenMatches(auth *cliproxyauth.Auth, apiKey string) bool {
	if auth == nil {
		return false
	}
	want := claudeAccountProfileTokenSHA256(apiKey)
	got := strings.ToLower(strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileTokenSHA256Key)))
	return want != "" && got == want
}

func (snapshot claudeAccountProfileSnapshot) usable(now time.Time) bool {
	if strings.TrimSpace(snapshot.accountUUID) == "" {
		return false
	}
	if snapshot.failedAt.IsZero() {
		return true
	}
	return !snapshot.failedAt.After(now) && now.Sub(snapshot.failedAt) <= claudeAccountProfileNegativeCacheTTL
}

func (snapshot claudeAccountProfileSnapshot) apply(auth *cliproxyauth.Auth) {
	if auth == nil {
		return
	}
	claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", snapshot.accountUUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "email", snapshot.email)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_uuid", snapshot.organizationUUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_name", snapshot.organizationName)
	claudeauth.StoreMetadataString(&auth.Metadata, claudeAccountProfileTokenSHA256Key, snapshot.tokenSHA256)
	if snapshot.failedAt.IsZero() {
		claudeauth.DeleteMetadataValue(&auth.Metadata, claudeAccountProfileFailedAtKey)
	} else {
		claudeauth.StoreMetadataString(&auth.Metadata, claudeAccountProfileFailedAtKey, snapshot.failedAt.UTC().Format(time.RFC3339))
	}
	clearClaudeAccountProfileLegacyMetadata(auth)
}

func storeClaudeAccountProfileSnapshot(auth *cliproxyauth.Auth, cacheEntry *claudeAccountProfileCacheEntry, snapshot claudeAccountProfileSnapshot) {
	snapshot.apply(auth)
	if cacheEntry != nil {
		cacheEntry.snapshot = snapshot
	}
}

func (e *ClaudeExecutor) accountProfileCacheEntry(auth *cliproxyauth.Auth, apiKey string) *claudeAccountProfileCacheEntry {
	if e == nil {
		return nil
	}
	key, ok := claudeAccountProfileCacheKey(auth, apiKey)
	if !ok {
		return nil
	}
	return e.accountProfileCache.entry(key)
}

// claudeAccountProfileFailureIsRecent reports whether this credential last
// failed a profile lookup within the negative-cache window. A missing,
// malformed, or future timestamp never suppresses a retry.
func claudeAccountProfileFailureIsRecent(auth *cliproxyauth.Auth, now time.Time) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	stampedAt, err := time.Parse(time.RFC3339, claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileFailedAtKey))
	if err != nil || stampedAt.After(now) {
		return false
	}
	return now.Sub(stampedAt) <= claudeAccountProfileNegativeCacheTTL
}

func claudeAccountProfileFailureIsPresent(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileFailedAtKey)) != ""
}

func claudeAccountProfileLegacyFallback(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	return strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileLegacyFallbackKey))
}

func claudeAccountProfileHasLegacyMetadata(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	return claudeAccountProfileLegacyFallback(auth) != "" ||
		strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileLegacyCheckedAtKey)) != ""
}

func clearClaudeAccountProfileLegacyMetadata(auth *cliproxyauth.Auth) {
	if auth == nil {
		return
	}
	claudeauth.DeleteMetadataValue(&auth.Metadata, claudeAccountProfileLegacyFallbackKey)
	claudeauth.DeleteMetadataValue(&auth.Metadata, claudeAccountProfileLegacyCheckedAtKey)
}

func newClaudeAccountProfileFallback(auth *cliproxyauth.Auth, apiKey, fallbackSeedPrefix string, failedAt time.Time) claudeAccountProfileSnapshot {
	seed := helps.ClaudeCLIAuthIdentitySeed(auth)
	if seed == "" {
		seed = fallbackSeedPrefix + "|" + apiKey
	}
	return claudeAccountProfileSnapshot{
		accountUUID: helps.StableClaudeCLIAccountUUID(seed),
		failedAt:    failedAt,
		tokenSHA256: claudeAccountProfileTokenSHA256(apiKey),
	}
}

func clearClaudeAccountProfileFailure(auth *cliproxyauth.Auth) {
	if auth == nil {
		return
	}
	claudeauth.DeleteMetadataValue(&auth.Metadata, claudeAccountProfileFailedAtKey)
	clearClaudeAccountProfileLegacyMetadata(auth)
}

type claudeOAuthProfileFetcher func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error)

func (e *ClaudeExecutor) ShouldPrepareRequestAuth(auth *cliproxyauth.Auth) bool {
	apiKey, _ := claudeCreds(auth)
	if !isClaudeOAuthToken(apiKey) || auth == nil {
		return false
	}
	if !claudeauth.HasCanonicalDeviceIDPool(claudeauth.ReadDeviceIDPool(&auth.Metadata)) {
		return true
	}
	// Run preparation once to collapse metadata written by the superseded
	// three-field negative-cache format.
	if claudeAccountProfileHasLegacyMetadata(auth) {
		return true
	}
	if helps.ClaudeCredentialAccountUUID(auth) == "" {
		return true
	}
	if !claudeAccountProfileTokenMatches(auth, apiKey) {
		return true
	}
	if !claudeAccountProfileFailureIsPresent(auth) {
		return false
	}
	return !claudeAccountProfileFailureIsRecent(auth, time.Now())
}

func isClaudeSetupToken(auth *cliproxyauth.Auth, apiKey string) bool {
	if !isClaudeOAuthToken(apiKey) || auth == nil {
		return false
	}
	if claudeauth.ReadMetadataBool(&auth.Metadata, "skip_account_profile") {
		return true
	}
	if claudeauth.ReadMetadataBool(&auth.Metadata, "is_setup_token") {
		return true
	}
	if claudeauth.ReadMetadataBool(&auth.Metadata, "setup_token") {
		return true
	}
	if kind := strings.ToLower(auth.Attributes["auth_kind"]); kind == "setup_token" || kind == "setup-token" {
		return true
	}
	scopes := strings.ToLower(claudeauth.ReadMetadataString(&auth.Metadata, "scopes"))
	if scopes == "" {
		scopes = strings.ToLower(claudeauth.ReadMetadataString(&auth.Metadata, "scope"))
	}
	if scopes != "" && !strings.Contains(scopes, "user:profile") && !strings.Contains(scopes, "user:office") {
		return true
	}
	return false
}

func (e *ClaudeExecutor) PrepareRequestAuth(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil || !e.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}
	apiKey, _ := claudeCreds(auth)
	claudeauth.EnsureMetadataMap(&auth.Metadata)
	if _, errDeviceIDs := helps.EnsureClaudeCredentialDevicePoolRequired(ctx, auth); errDeviceIDs != nil {
		return nil, errDeviceIDs
	}
	now := time.Now()
	clearClaudeAccountProfileLegacyMetadata(auth)
	hasAccountUUID := helps.ClaudeCredentialAccountUUID(auth) != ""
	bindingMatches := claudeAccountProfileTokenMatches(auth, apiKey)
	if hasAccountUUID && bindingMatches && claudeAccountProfileFailureIsRecent(auth, now) {
		return auth, nil
	}
	if hasAccountUUID && bindingMatches && !claudeAccountProfileFailureIsPresent(auth) {
		return auth, nil
	}

	if isClaudeSetupToken(auth, apiKey) {
		newClaudeAccountProfileFallback(auth, apiKey, "claude-setup-token", time.Time{}).apply(auth)
		return auth, nil
	}

	cacheEntry := e.accountProfileCacheEntry(auth, apiKey)
	if cacheEntry != nil {
		cacheEntry.mu.Lock()
		defer cacheEntry.mu.Unlock()
		now = time.Now()
		// Persisted local state is authoritative. Reuse an in-memory snapshot only
		// when a fresh Home dispatch omitted the prepared account identity.
		if !hasAccountUUID && cacheEntry.snapshot.usable(now) {
			cacheEntry.snapshot.apply(auth)
			return auth, nil
		}
		cacheEntry.snapshot = claudeAccountProfileSnapshot{}
	}

	profile, errProfile := e.fetchClaudeOAuthProfile(ctx, auth, apiKey)
	if errProfile != nil {
		if errContext := ctx.Err(); errContext != nil {
			return nil, errContext
		}
		if claudeauth.IsOAuthProfileScopeError(errProfile) {
			log.Debugf("Claude OAuth account profile lookup confirmed a missing profile scope for auth %s: %v (using stable credential identity)", auth.ID, errProfile)
			snapshot := newClaudeAccountProfileFallback(auth, apiKey, "claude-oauth-fallback", time.Time{})
			storeClaudeAccountProfileSnapshot(auth, cacheEntry, snapshot)
			return auth, nil
		}
		// Persist a usable deterministic identity with the failure stamp. Returning
		// success is intentional: Manager only commits request-auth clones when
		// preparation succeeds, and the inference endpoint remains usable without
		// the optional profile response.
		snapshot := newClaudeAccountProfileFallback(auth, apiKey, "claude-oauth-fallback", now)
		storeClaudeAccountProfileSnapshot(auth, cacheEntry, snapshot)
		log.Debugf("Claude OAuth account profile lookup failed for auth %s: %v (using stable credential identity for %s)", auth.ID, errProfile, claudeAccountProfileNegativeCacheTTL)
		return auth, nil
	}
	if profile == nil || strings.TrimSpace(profile.Account.UUID) == "" {
		log.Debugf("Claude OAuth account profile lookup returned empty account UUID for auth %s (falling back to stable credential identity)", auth.ID)
		snapshot := newClaudeAccountProfileFallback(auth, apiKey, "claude-oauth-fallback", now)
		storeClaudeAccountProfileSnapshot(auth, cacheEntry, snapshot)
		return auth, nil
	}
	snapshot := claudeAccountProfileSnapshot{
		accountUUID:      profile.Account.UUID,
		email:            profile.Account.Email,
		organizationUUID: profile.Organization.UUID,
		organizationName: profile.Organization.Name,
		tokenSHA256:      claudeAccountProfileTokenSHA256(apiKey),
	}
	storeClaudeAccountProfileSnapshot(auth, cacheEntry, snapshot)
	return auth, nil
}

func (e *ClaudeExecutor) fetchClaudeOAuthProfile(ctx context.Context, auth *cliproxyauth.Auth, apiKey string) (*claudeauth.OAuthProfile, error) {
	if e == nil {
		return nil, fmt.Errorf("fetch Claude OAuth profile: executor is nil")
	}
	if e.oauthProfileFetcher != nil {
		return e.oauthProfileFetcher(ctx, auth, apiKey)
	}
	if auth == nil {
		return nil, fmt.Errorf("fetch Claude OAuth profile: auth is nil")
	}
	profileCtx, cancelProfile := context.WithTimeout(ctx, claudeAccountProfileTimeout)
	defer cancelProfile()
	service := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
	return service.FetchOAuthProfile(profileCtx, apiKey)
}

func (e *ClaudeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("claude executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("claude executor: auth is nil")
	}
	refreshToken := claudeauth.ReadMetadataString(&auth.Metadata, "refresh_token")
	if refreshToken == "" {
		refreshToken = claudeauth.ReadMetadataString(&auth.Metadata, "refreshToken")
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := claudeauth.NewClaudeAuthWithProxyURL(e.cfg, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	claudeauth.EnsureMetadataMap(&auth.Metadata)
	claudeauth.StoreMetadataValue(&auth.Metadata, "access_token", td.AccessToken)
	claudeauth.StoreMetadataString(&auth.Metadata, "refresh_token", td.RefreshToken)
	// Profile fields are optional when token rotation succeeds but the follow-up
	// profile lookup fails. Never erase the previously resolved credential identity.
	claudeauth.StoreMetadataString(&auth.Metadata, "email", td.Email)
	claudeauth.StoreMetadataString(&auth.Metadata, "account_uuid", td.AccountUUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_uuid", td.OrganizationUUID)
	claudeauth.StoreMetadataString(&auth.Metadata, "organization_name", td.OrganizationName)
	updateClaudeAccountProfileBindingAfterRefresh(auth, td.AccessToken, td.AccountUUID)
	claudeauth.StoreMetadataValue(&auth.Metadata, "expired", td.Expire)
	claudeauth.StoreMetadataValue(&auth.Metadata, "type", "claude")
	claudeauth.StoreMetadataValue(&auth.Metadata, "last_refresh", time.Now().Format(time.RFC3339))
	return auth, nil
}

func updateClaudeAccountProfileBindingAfterRefresh(auth *cliproxyauth.Auth, accessToken, accountUUID string) {
	if auth == nil {
		return
	}
	if strings.TrimSpace(accountUUID) != "" {
		claudeauth.StoreMetadataString(&auth.Metadata, claudeAccountProfileTokenSHA256Key, claudeAccountProfileTokenSHA256(accessToken))
		clearClaudeAccountProfileFailure(auth)
		return
	}
	// RefreshTokens already attempted the profile lookup. When it returned no
	// identity, force request preparation for the new token instead of carrying
	// an old token's cached result across the credential generation boundary.
	claudeauth.DeleteMetadataValue(&auth.Metadata, claudeAccountProfileTokenSHA256Key)
	clearClaudeAccountProfileFailure(auth)
}
