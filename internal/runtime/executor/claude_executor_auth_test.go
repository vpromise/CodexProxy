package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type claudeProfilePrepareStore struct {
	mu    sync.Mutex
	saves []*cliproxyauth.Auth
}

func (s *claudeProfilePrepareStore) List(context.Context) ([]*cliproxyauth.Auth, error) {
	return nil, nil
}

func (s *claudeProfilePrepareStore) Save(_ context.Context, auth *cliproxyauth.Auth) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves = append(s.saves, auth.Clone())
	return auth.ID, nil
}

func (s *claudeProfilePrepareStore) Delete(context.Context, string) error { return nil }

func (s *claudeProfilePrepareStore) last() *cliproxyauth.Auth {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.saves) == 0 {
		return nil
	}
	return s.saves[len(s.saves)-1].Clone()
}

func TestClaudeExecutorDuplicateMetadataIsRequestScoped(t *testing.T) {
	testCases := []struct {
		name string
		run  func(context.Context, *ClaudeExecutor, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) error
	}{
		{
			name: "execute",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errExecute := executor.Execute(ctx, auth, req, opts)
				return errExecute
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context, executor *ClaudeExecutor, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) error {
				_, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				return errStream
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			upstreamCalled := false
			transport := roundTripperFunc(func(*http.Request) (*http.Response, error) {
				upstreamCalled = true
				return nil, errors.New("unexpected upstream request")
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "sk-ant-oat-duplicate-metadata", "auth_kind": "oauth"},
				Metadata: map[string]any{
					"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
					claudeauth.ClaudeDeviceIDsMetadataKey: []string{
						"0000000000000000000000000000000000000000000000000000000000000000",
					},
				},
			}
			req := cliproxyexecutor.Request{
				Model: "claude-opus-5",
				Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello"}],` +
					`"metadata":{"user_id":"{}"},"metadata":{"user_id":"{}"}}`),
			}
			errRun := testCase.run(ctx, NewClaudeExecutor(&config.Config{}), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
			if errRun == nil {
				t.Fatal("duplicate metadata error = nil")
			}
			if upstreamCalled {
				t.Fatal("duplicate metadata reached upstream")
			}
			var requestErr cliproxyexecutor.RequestScopedError
			if !errors.As(errRun, &requestErr) || requestErr == nil || !requestErr.IsRequestScoped() {
				t.Fatalf("duplicate metadata error = %T %v, want request-scoped", errRun, errRun)
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errRun, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("duplicate metadata error = %T %v, want HTTP 400", errRun, errRun)
			}
		})
	}
}

func TestClaudeExecutorPrepareRequestAuthPopulatesCredentialIdentity(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(_ context.Context, _ *cliproxyauth.Auth, accessToken string) (*claudeauth.OAuthProfile, error) {
		if accessToken != "sk-ant-oat-prepare" {
			t.Fatalf("access token = %q, want selected credential token", accessToken)
		}
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		profile.Account.Email = "user@example.com"
		profile.Organization.UUID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
		profile.Organization.Name = "Example Org"
		return profile, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-old-credential",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat-prepare",
		},
		Metadata: map[string]any{"type": "claude"},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing credential identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs := claudeauth.NormalizeDeviceIDPool(prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	if len(deviceIDs) != claudeauth.ClaudeDevicePoolSize {
		t.Fatalf("device pool length = %d, want %d", len(deviceIDs), claudeauth.ClaudeDevicePoolSize)
	}
	if got := prepared.Metadata["account_uuid"]; got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account_uuid = %#v, want upstream profile account", got)
	}
	if got := prepared.Metadata["organization_uuid"]; got != "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb" {
		t.Fatalf("organization_uuid = %#v, want upstream profile organization", got)
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthMigratesFiveDevicesToOne(t *testing.T) {
	legacy := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
		"3333333333333333333333333333333333333333333333333333333333333333",
		"4444444444444444444444444444444444444444444444444444444444444444",
	}
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile lookup should not run when account UUID is already present")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID:         "claude-five-device-credential",
		Attributes: map[string]string{"api_key": "sk-ant-oat-five-device"},
		Metadata: map[string]any{
			"account_uuid":                        "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			claudeAccountProfileTokenSHA256Key:    claudeAccountProfileTokenSHA256("sk-ant-oat-five-device"),
			claudeauth.ClaudeDeviceIDsMetadataKey: legacy,
		},
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for legacy five-device pool")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	deviceIDs, ok := prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey].([]string)
	if !ok || len(deviceIDs) != 1 || deviceIDs[0] != legacy[0] {
		t.Fatalf("prepared device IDs = %#v, want first legacy device only", prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after single-device migration")
	}
}

func TestClaudeExecutorPrepareRequestAuthTemporaryFailureCreatesStableFallback(t *testing.T) {
	calls := 0
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls++
		return nil, fmt.Errorf("profile unavailable")
	}
	auth := &cliproxyauth.Auth{
		ID:         "claude-profile-unavailable",
		Attributes: map[string]string{"api_key": "sk-ant-oat-profile-unavailable"},
		Metadata: map[string]any{
			"type":                                 "claude",
			claudeAccountProfileLegacyCheckedAtKey: "2999-01-01T00:00:00Z",
			claudeAccountProfileLegacyFallbackKey:  "temporary_error",
			claudeauth.ClaudeDeviceIDsMetadataKey:  []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for a future checked timestamp; future stamps must not suppress retry")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v, want usable fallback", errPrepare)
	}
	if prepared == nil {
		t.Fatal("PrepareRequestAuth() auth = nil, want usable fallback")
	}
	if calls != 1 {
		t.Fatalf("profile calls = %d, want 1", calls)
	}
	wantAccountUUID := helps.StableClaudeCLIAccountUUID(helps.ClaudeCLIAuthIdentitySeed(auth))
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid"); got != wantAccountUUID {
		t.Fatalf("fallback account UUID = %q, want stable %q", got, wantAccountUUID)
	}
	failedAt := claudeauth.ReadMetadataString(&prepared.Metadata, claudeAccountProfileFailedAtKey)
	if _, errParse := time.Parse(time.RFC3339, failedAt); errParse != nil {
		t.Fatalf("profile failure timestamp = %q, want RFC3339", failedAt)
	}
	if claudeAccountProfileHasLegacyMetadata(prepared) {
		t.Fatal("legacy profile metadata survived migration")
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true inside the failure negative-cache window")
	}
	preparedAgain, errPrepareAgain := executor.PrepareRequestAuth(context.Background(), prepared.Clone())
	if errPrepareAgain != nil {
		t.Fatalf("second PrepareRequestAuth() error = %v", errPrepareAgain)
	}
	if calls != 1 {
		t.Fatalf("profile calls after cached preparation = %d, want 1", calls)
	}
	if got := claudeauth.ReadMetadataString(&preparedAgain.Metadata, "account_uuid"); got != wantAccountUUID {
		t.Fatalf("cached fallback account UUID = %q, want stable %q", got, wantAccountUUID)
	}
}

func TestClaudeExecutorPrepareRequestAuthFailureOlderThanTTLRetriesAndClearsFallback(t *testing.T) {
	calls := 0
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls++
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		return profile, nil
	}
	fallbackUUID := helps.StableClaudeCLIAccountUUID("auth-id|claude-profile-stale-failure")
	auth := &cliproxyauth.Auth{
		ID:         "claude-profile-stale-failure",
		Attributes: map[string]string{"api_key": "sk-ant-oat-stale-failure"},
		Metadata: map[string]any{
			"type":                                "claude",
			"account_uuid":                        fallbackUUID,
			claudeAccountProfileTokenSHA256Key:    claudeAccountProfileTokenSHA256("sk-ant-oat-stale-failure"),
			claudeAccountProfileFailedAtKey:       time.Now().Add(-2 * claudeAccountProfileNegativeCacheTTL).UTC().Format(time.RFC3339),
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for a failure older than the negative-cache TTL")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if calls != 1 {
		t.Fatalf("profile calls = %d, want 1", calls)
	}
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid"); got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account UUID after retry = %q, want upstream profile", got)
	}
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, claudeAccountProfileFailedAtKey); got != "" {
		t.Fatalf("profile failure timestamp after success = %q, want cleared", got)
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after successful retry")
	}
}

func TestClaudeExecutorPrepareRequestAuthRevalidatesLegacyFailureWithoutTokenBinding(t *testing.T) {
	calls := 0
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls++
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		return profile, nil
	}
	auth := &cliproxyauth.Auth{
		ID:         "claude-profile-recent-failure",
		Attributes: map[string]string{"api_key": "sk-ant-oat-recent-failure"},
		Metadata: map[string]any{
			"type":                                 "claude",
			"account_uuid":                         helps.StableClaudeCLIAccountUUID("auth-id|claude-profile-recent-failure"),
			claudeAccountProfileFailedAtKey:        time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339),
			claudeAccountProfileLegacyCheckedAtKey: "2026-01-01T00:00:00Z",
			claudeAccountProfileLegacyFallbackKey:  "temporary_error",
			claudeauth.ClaudeDeviceIDsMetadataKey:  []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}
	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false while legacy metadata needs migration")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if prepared != auth {
		t.Fatalf("PrepareRequestAuth() = %#v, want the credential passed through unchanged", prepared)
	}
	if calls != 1 {
		t.Fatalf("profile calls = %d, want 1 for unbound legacy state", calls)
	}
	if claudeAccountProfileHasLegacyMetadata(prepared) {
		t.Fatal("legacy profile metadata survived migration")
	}
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid"); got != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatalf("account UUID after revalidation = %q", got)
	}
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, claudeAccountProfileFailedAtKey); got != "" {
		t.Fatalf("failure timestamp after revalidation = %q, want cleared", got)
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after binding the revalidated profile")
	}
}

func TestClaudeExecutorHomeSnapshotsReuseBoundedProfileCache(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	var calls int
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls++
		return nil, fmt.Errorf("proxy returned status 403 without a profile scope error")
	}
	// Home returns a newly decoded Auth for every dispatch, so each call uses a
	// distinct snapshot rather than reusing the object mutated by preparation.
	newSnapshot := func(id, token, proxyURL string) *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			ID:       id,
			Provider: "claude",
			ProxyURL: proxyURL,
			Attributes: map[string]string{
				"api_key":   token,
				"auth_kind": "oauth",
			},
			Metadata: map[string]any{
				"type":                                "claude",
				claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
			},
		}
	}

	first, errFirst := executor.PrepareRequestAuth(context.Background(), newSnapshot("home-cred", "sk-ant-oat-home-a", ""))
	if errFirst != nil {
		t.Fatalf("first Home snapshot preparation: %v", errFirst)
	}
	second, errSecond := executor.PrepareRequestAuth(context.Background(), newSnapshot("home-cred", "sk-ant-oat-home-a", ""))
	if errSecond != nil {
		t.Fatalf("second Home snapshot preparation: %v", errSecond)
	}
	if calls != 1 {
		t.Fatalf("profile fetches for equal Home snapshots = %d, want 1", calls)
	}
	firstUUID := claudeauth.ReadMetadataString(&first.Metadata, "account_uuid")
	secondUUID := claudeauth.ReadMetadataString(&second.Metadata, "account_uuid")
	if firstUUID == "" || secondUUID != firstUUID {
		t.Fatalf("cached Home identity mismatch: first=%q second=%q", firstUUID, secondUUID)
	}
	if got := claudeauth.ReadMetadataString(&second.Metadata, claudeAccountProfileFailedAtKey); got == "" {
		t.Fatal("ambiguous 403 was cached permanently; want a temporary failure stamp")
	}

	for _, snapshot := range []*cliproxyauth.Auth{
		newSnapshot("home-cred", "sk-ant-oat-home-b", ""),
		newSnapshot("home-cred-other", "sk-ant-oat-home-a", ""),
		newSnapshot("home-cred", "sk-ant-oat-home-a", "http://127.0.0.1:9080"),
	} {
		if _, errPrepare := executor.PrepareRequestAuth(context.Background(), snapshot); errPrepare != nil {
			t.Fatalf("isolated Home snapshot preparation: %v", errPrepare)
		}
	}
	if calls != 4 {
		t.Fatalf("profile fetches after token/identity/proxy partitions = %d, want 4", calls)
	}
}

func TestClaudeExecutorHomeSnapshotsDeduplicateConcurrentProfileFailure(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	var calls atomic.Int32
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		calls.Add(1)
		return nil, errors.New("profile unavailable")
	}
	newSnapshot := func() *cliproxyauth.Auth {
		return &cliproxyauth.Auth{
			ID:         "home-concurrent-profile",
			Provider:   "claude",
			Attributes: map[string]string{"api_key": "sk-ant-oat-home-concurrent", "auth_kind": "oauth"},
			Metadata: map[string]any{
				"type":                                "claude",
				claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
			},
		}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), newSnapshot())
			if errPrepare != nil {
				t.Errorf("PrepareRequestAuth() error = %v", errPrepare)
				return
			}
			if got := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid"); got == "" {
				t.Error("cached profile fallback has empty account UUID")
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent profile fetches = %d, want 1", got)
	}
}

func TestClaudeExecutorProfileBindingDoesNotCrossTokenGenerations(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		firstFailure func() error
		wantFailedAt bool
	}{
		{name: "temporary", firstFailure: func() error { return errors.New("profile unavailable") }, wantFailedAt: true},
		{
			name: "scope",
			firstFailure: func() error {
				return claudeauth.NewOAuthHTTPStatusError(
					"fetch Claude OAuth profile",
					http.StatusForbidden,
					"permission_error",
					"OAuth token does not meet scope requirement user:profile",
				)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const (
				firstToken  = "sk-ant-oat-generation-a"
				secondToken = "sk-ant-oat-generation-b"
				realUUID    = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
			)
			executor := NewClaudeExecutor(&config.Config{})
			calls := 0
			executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
				calls++
				if calls == 1 {
					return nil, testCase.firstFailure()
				}
				profile := &claudeauth.OAuthProfile{}
				profile.Account.UUID = realUUID
				return profile, nil
			}
			auth := &cliproxyauth.Auth{
				ID:         "token-generation-" + testCase.name,
				Provider:   "claude",
				Attributes: map[string]string{"api_key": firstToken, "auth_kind": "oauth"},
				Metadata: map[string]any{
					"type":                                "claude",
					claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
				},
			}

			first, errFirst := executor.PrepareRequestAuth(context.Background(), auth)
			if errFirst != nil {
				t.Fatalf("first generation preparation: %v", errFirst)
			}
			if got := claudeauth.ReadMetadataString(&first.Metadata, claudeAccountProfileTokenSHA256Key); got != claudeAccountProfileTokenSHA256(firstToken) {
				t.Fatalf("first token binding = %q", got)
			}
			if got := claudeauth.ReadMetadataString(&first.Metadata, claudeAccountProfileFailedAtKey); (got != "") != testCase.wantFailedAt {
				t.Fatalf("first failed_at present = %t, want %t", got != "", testCase.wantFailedAt)
			}

			first.Attributes["api_key"] = secondToken
			if !executor.ShouldPrepareRequestAuth(first) {
				t.Fatal("new token generation reused the previous profile binding")
			}
			second, errSecond := executor.PrepareRequestAuth(context.Background(), first)
			if errSecond != nil {
				t.Fatalf("second generation preparation: %v", errSecond)
			}
			if calls != 2 {
				t.Fatalf("profile fetches = %d, want 2 across token generations", calls)
			}
			if got := claudeauth.ReadMetadataString(&second.Metadata, "account_uuid"); got != realUUID {
				t.Fatalf("second generation account UUID = %q, want %q", got, realUUID)
			}
			if got := claudeauth.ReadMetadataString(&second.Metadata, claudeAccountProfileTokenSHA256Key); got != claudeAccountProfileTokenSHA256(secondToken) {
				t.Fatalf("second token binding = %q", got)
			}
			if got := claudeauth.ReadMetadataString(&second.Metadata, claudeAccountProfileFailedAtKey); got != "" {
				t.Fatalf("second generation failure timestamp = %q, want cleared", got)
			}
		})
	}
}

func TestUpdateClaudeAccountProfileBindingAfterRefreshWithoutProfile(t *testing.T) {
	const (
		oldToken = "sk-ant-oat-refresh-old"
		newToken = "sk-ant-oat-refresh-new"
	)
	auth := &cliproxyauth.Auth{
		ID:       "refresh-profile-binding",
		Provider: "claude",
		Metadata: map[string]any{
			"access_token":                        newToken,
			"account_uuid":                        helps.StableClaudeCLIAccountUUID("auth-id|refresh-profile-binding"),
			claudeAccountProfileTokenSHA256Key:    claudeAccountProfileTokenSHA256(oldToken),
			claudeAccountProfileFailedAtKey:       time.Now().UTC().Format(time.RFC3339),
			claudeauth.ClaudeDeviceIDsMetadataKey: []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		},
	}

	updateClaudeAccountProfileBindingAfterRefresh(auth, newToken, "")
	if got := claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileTokenSHA256Key); got != "" {
		t.Fatalf("token binding after profile-less refresh = %q, want cleared", got)
	}
	if got := claudeauth.ReadMetadataString(&auth.Metadata, claudeAccountProfileFailedAtKey); got != "" {
		t.Fatalf("failure timestamp after profile-less refresh = %q, want cleared", got)
	}
	if !NewClaudeExecutor(&config.Config{}).ShouldPrepareRequestAuth(auth) {
		t.Fatal("profile-less refresh did not force preparation for the new token")
	}
}

func TestClaudeExecutorManagerExecutePersistsTemporaryProfileFallbackAndRetriesAfterTTL(t *testing.T) {
	const (
		model               = "claude-3-5-sonnet-20241022"
		upstreamAccountUUID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	)
	store := &claudeProfilePrepareStore{}
	executor := NewClaudeExecutor(&config.Config{})
	profileCalls := 0
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		profileCalls++
		if profileCalls == 1 {
			return nil, fmt.Errorf("profile service unavailable")
		}
		profile := &claudeauth.OAuthProfile{}
		profile.Account.UUID = upstreamAccountUUID
		profile.Account.Email = "profile@example.com"
		return profile, nil
	}

	var requestMu sync.Mutex
	requestAccountUUIDs := make([]string, 0, 3)
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		var payload struct {
			Metadata struct {
				UserID string `json:"user_id"`
			} `json:"metadata"`
		}
		if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
			return nil, fmt.Errorf("decode upstream request: %w", errUnmarshal)
		}
		var identity struct {
			AccountUUID string `json:"account_uuid"`
		}
		if errUnmarshal := json.Unmarshal([]byte(payload.Metadata.UserID), &identity); errUnmarshal != nil {
			return nil, fmt.Errorf("decode upstream identity: %w", errUnmarshal)
		}
		if identity.AccountUUID == "" {
			return nil, fmt.Errorf("upstream identity has an empty account UUID")
		}
		requestMu.Lock()
		requestAccountUUIDs = append(requestAccountUUIDs, identity.AccountUUID)
		requestMu.Unlock()
		responseBody := `{"id":"msg_profile","type":"message","role":"assistant","model":"` + model + `","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(responseBody)),
			Request:    req,
		}, nil
	})
	executionCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	auth := &cliproxyauth.Auth{
		ID:       "claude-profile-negative-cache-manager",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":   "sk-ant-oat-profile-negative-cache",
			"auth_kind": "oauth",
		},
		Metadata: map[string]any{"type": "claude"},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	newManager := func(initial *cliproxyauth.Auth) *cliproxyauth.Manager {
		manager := cliproxyauth.NewManager(store, nil, nil)
		manager.SetRetryConfig(0, 0, 1)
		manager.RegisterExecutor(executor)
		if _, errRegister := manager.Register(cliproxyauth.WithSkipPersist(context.Background()), initial); errRegister != nil {
			t.Fatalf("register auth: %v", errRegister)
		}
		return manager
	}
	execute := func(manager *cliproxyauth.Manager) {
		t.Helper()
		_, errExecute := manager.Execute(executionCtx, []string{"claude"}, cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"model":"` + model + `","max_tokens":16,"messages":[{"role":"user","content":"hello"}]}`),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
		if errExecute != nil {
			t.Fatalf("Manager.Execute() error = %v", errExecute)
		}
	}

	firstManager := newManager(auth)
	execute(firstManager)
	if profileCalls != 1 {
		t.Fatalf("profile calls after first Execute = %d, want 1", profileCalls)
	}
	if got := claudeauth.ReadMetadataString(&auth.Metadata, "account_uuid"); got != "" {
		t.Fatalf("registered input pointer account UUID = %q, want unchanged clone source", got)
	}
	persistedFallback := store.last()
	if persistedFallback == nil {
		t.Fatal("temporary fallback was not persisted")
	}
	fallbackUUID := claudeauth.ReadMetadataString(&persistedFallback.Metadata, "account_uuid")
	if fallbackUUID == "" || fallbackUUID == upstreamAccountUUID {
		t.Fatalf("persisted fallback account UUID = %q, want generated identity", fallbackUUID)
	}
	if got := claudeauth.ReadMetadataString(&persistedFallback.Metadata, claudeAccountProfileFailedAtKey); got == "" {
		t.Fatal("persisted profile failure timestamp is empty")
	}
	if got := claudeauth.ReadMetadataString(&persistedFallback.Metadata, claudeAccountProfileTokenSHA256Key); got != claudeAccountProfileTokenSHA256("sk-ant-oat-profile-negative-cache") {
		t.Fatalf("persisted profile token binding = %q", got)
	}
	if claudeAccountProfileHasLegacyMetadata(persistedFallback) {
		t.Fatal("persisted fallback contains legacy profile metadata")
	}

	// Reload the persisted clone into a fresh Manager. The negative cache must
	// survive that boundary and keep the fallback identity usable without a
	// second profile request.
	secondManager := newManager(persistedFallback)
	execute(secondManager)
	if profileCalls != 1 {
		t.Fatalf("profile calls after reloaded cached Execute = %d, want 1", profileCalls)
	}

	staleFallback, ok := secondManager.GetByID(auth.ID)
	if !ok {
		t.Fatal("reloaded auth is missing")
	}
	claudeauth.StoreMetadataString(&staleFallback.Metadata, claudeAccountProfileFailedAtKey, time.Now().Add(-2*claudeAccountProfileNegativeCacheTTL).UTC().Format(time.RFC3339))
	if _, errUpdate := secondManager.Update(context.Background(), staleFallback); errUpdate != nil {
		t.Fatalf("make fallback stale: %v", errUpdate)
	}
	execute(secondManager)
	if profileCalls != 2 {
		t.Fatalf("profile calls after expired cached Execute = %d, want 2", profileCalls)
	}

	resolved, ok := secondManager.GetByID(auth.ID)
	if !ok {
		t.Fatal("resolved auth is missing")
	}
	if got := claudeauth.ReadMetadataString(&resolved.Metadata, "account_uuid"); got != upstreamAccountUUID {
		t.Fatalf("resolved account UUID = %q, want %q", got, upstreamAccountUUID)
	}
	if got := claudeauth.ReadMetadataString(&resolved.Metadata, claudeAccountProfileFailedAtKey); got != "" {
		t.Fatalf("resolved failure timestamp = %q, want cleared", got)
	}
	finalPersisted := store.last()
	if finalPersisted == nil || claudeauth.ReadMetadataString(&finalPersisted.Metadata, "account_uuid") != upstreamAccountUUID {
		t.Fatalf("final persisted auth = %#v, want resolved upstream identity", finalPersisted)
	}
	if _, exists := finalPersisted.Metadata[claudeAccountProfileFailedAtKey]; exists {
		t.Fatal("final persisted auth still contains failure timestamp")
	}

	requestMu.Lock()
	gotRequestAccountUUIDs := append([]string(nil), requestAccountUUIDs...)
	requestMu.Unlock()
	wantRequestAccountUUIDs := []string{fallbackUUID, fallbackUUID, upstreamAccountUUID}
	if len(gotRequestAccountUUIDs) != len(wantRequestAccountUUIDs) {
		t.Fatalf("upstream request account UUIDs = %#v, want %#v", gotRequestAccountUUIDs, wantRequestAccountUUIDs)
	}
	for i := range wantRequestAccountUUIDs {
		if gotRequestAccountUUIDs[i] != wantRequestAccountUUIDs[i] {
			t.Fatalf("upstream request account UUIDs = %#v, want %#v", gotRequestAccountUUIDs, wantRequestAccountUUIDs)
		}
	}
}

func TestClaudeExecutorPrepareRequestAuthSetupTokenBypassesProfile(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile fetcher should NOT be called for setup-tokens")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-setuptoken.json",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-test-setup-token-value",
		},
		Metadata: map[string]any{
			"type":   "claude",
			"scopes": "user:inference user:ccr_inference user:file_upload",
		},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing setup-token identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after setup-token preparation")
	}
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, claudeAccountProfileFailedAtKey); got != "" {
		t.Fatalf("setup-token failure timestamp = %q, want empty", got)
	}
	deviceIDs := claudeauth.NormalizeDeviceIDPool(prepared.Metadata[claudeauth.ClaudeDeviceIDsMetadataKey])
	if len(deviceIDs) != 1 {
		t.Fatalf("device pool length = %d, want 1", len(deviceIDs))
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after setup-token identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuth403ScopeFallback(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	fetchCalls := 0
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		fetchCalls++
		return nil, claudeauth.NewOAuthHTTPStatusError(
			"fetch Claude OAuth profile",
			http.StatusForbidden,
			"permission_error",
			"OAuth token does not meet scope requirement any_of(user:profile, user:office)",
		)
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-scope-restricted-credential",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-scope-restricted",
		},
		Metadata: map[string]any{
			"type":          "claude",
			"refresh_token": "dummy-refresh-token",
		},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() with 403 error = %v, want fallback success", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d, want 1", fetchCalls)
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after 403 fallback")
	}
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, claudeAccountProfileFailedAtKey); got != "" {
		t.Fatalf("403 failure timestamp = %q, want empty", got)
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after 403 identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthSkipAccountProfileConfig(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		t.Fatal("profile fetcher should NOT be called when skip_account_profile is true")
		return nil, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-skip-profile.json",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-skip-profile",
		},
		Metadata: map[string]any{
			"type":                 "claude",
			"skip_account_profile": true,
		},
	}

	if !executor.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for missing identity")
	}
	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after skip_account_profile preparation")
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}

func TestClaudeExecutorPrepareRequestAuthEmptyAccountUUIDInProfileFallback(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{})
	fetchCalls := 0
	executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
		fetchCalls++
		return &claudeauth.OAuthProfile{}, nil
	}
	auth := &cliproxyauth.Auth{
		ID: "claude-empty-uuid-in-profile",
		Attributes: map[string]string{
			"api_key": "sk-ant-oat01-empty-uuid",
		},
		Metadata: map[string]any{
			"type": "claude",
		},
	}

	prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v, want fallback on empty UUID", errPrepare)
	}
	if prepared == nil {
		t.Fatal("prepared auth is nil")
	}
	if fetchCalls != 1 {
		t.Fatalf("fetchCalls = %d, want 1", fetchCalls)
	}
	accountUUID := claudeauth.ReadMetadataString(&prepared.Metadata, "account_uuid")
	if accountUUID == "" {
		t.Fatal("account_uuid is empty after fallback")
	}
	if got := claudeauth.ReadMetadataString(&prepared.Metadata, claudeAccountProfileFailedAtKey); got == "" {
		t.Fatal("empty-profile failure timestamp is empty")
	}
	if executor.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after identity was populated")
	}
}
