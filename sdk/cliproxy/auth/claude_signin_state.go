package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// ClaudeSignInRecord persists a confirmed refresh expiry for one credential.
// AccessRejected records its separate, confirmed rejection on the messages route.
type ClaudeSignInRecord struct {
	CredentialFingerprint string `json:"credential_fingerprint"`
	AccessRejected        bool   `json:"access_rejected,omitempty"`
}

type claudeSignInStore interface {
	LoadClaudeSignIn(*Auth) (ClaudeSignInRecord, error)
	SaveClaudeSignIn(*Auth, ClaudeSignInRecord) error
}

// ClaudeSignInFingerprint identifies the Claude OAuth token snapshot across
// a restart without retaining any token values in the persisted sign-in state.
func ClaudeSignInFingerprint(auth *Auth) string {
	if auth == nil || !strings.EqualFold(auth.Provider, "claude") || auth.AuthKind() != AuthKindOAuth {
		return ""
	}
	refreshToken := authRefreshToken(auth)
	if refreshToken == "" {
		return ""
	}
	idToken := authMetadataString(auth, "id_token")
	if idToken == "" {
		idToken = authMetadataString(auth, "idToken")
	}
	values, _ := json.Marshal([3]string{refreshToken, authAccessToken(auth), idToken})
	sum := sha256.Sum256(values)
	return hex.EncodeToString(sum[:])
}

func localClaudeSignInPath(auth *Auth) string {
	if auth == nil || auth.AuthSourceKind() != AuthSourceFile ||
		auth.Attributes[AttributeSourceBackend] != AuthSourceFile || auth.Storage != nil ||
		IsPluginVirtualAuth(auth) || auth.Attributes["plugin_id"] != "" || auth.Attributes["compat_name"] != "" {
		return ""
	}
	return authAttribute(auth, AttributePath)
}

func restoreClaudeSignIn(store Store, auth *Auth) {
	localStore, ok := store.(claudeSignInStore)
	if !ok || localClaudeSignInPath(auth) == "" {
		return
	}
	fingerprint := ClaudeSignInFingerprint(auth)
	if fingerprint == "" {
		return
	}
	record, errLoad := localStore.LoadClaudeSignIn(auth)
	if errLoad != nil {
		log.WithField("auth_id", auth.ID).WithError(errLoad).Warn("cannot restore local Claude sign-in state")
		return
	}
	if record.CredentialFingerprint == fingerprint {
		if record.AccessRejected {
			auth.claudeRefreshState = claudeRefreshAccessRejected
		} else if !auth.RequiresSignIn() {
			auth.claudeRefreshState = claudeRefreshSignInRequired
		}
		auth.NextRefreshAfter = time.Time{}
	}
}

func (m *Manager) persistClaudeSignIn(ctx context.Context, snapshot *Auth) {
	path := localClaudeSignInPath(snapshot)
	if path == "" || shouldSkipPersist(ctx) {
		return
	}
	m.storeReloadMu.RLock()
	defer m.storeReloadMu.RUnlock()
	pLock := m.authPersistenceLock(snapshot.ID)
	pLock.mu.Lock()
	defer pLock.mu.Unlock()

	m.mu.RLock()
	store, ok := m.store.(claudeSignInStore)
	current := m.auths[snapshot.ID]
	if !ok || !current.RequiresSignIn() || !sameCredentialSnapshot(snapshot, current) || localClaudeSignInPath(current) != path {
		m.mu.RUnlock()
		return
	}
	current = current.Clone()
	m.mu.RUnlock()
	fingerprint := ClaudeSignInFingerprint(current)
	if fingerprint == "" {
		return
	}
	if errSave := store.SaveClaudeSignIn(current, ClaudeSignInRecord{
		CredentialFingerprint: fingerprint,
		AccessRejected:        current.claudeRefreshState == claudeRefreshAccessRejected,
	}); errSave != nil {
		log.WithField("auth_id", current.ID).WithError(errSave).Warn("cannot persist local Claude sign-in state")
	}
}
