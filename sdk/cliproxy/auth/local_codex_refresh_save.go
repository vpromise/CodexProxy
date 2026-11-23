package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

type localCodexRefreshSaveError struct {
	cause          error
	diskDigest     [sha256.Size]byte
	diskCredential [sha256.Size]byte
	retry          func(context.Context, *Auth) (bool, error)
}

func (e *localCodexRefreshSaveError) Error() string { return e.cause.Error() }
func (e *localCodexRefreshSaveError) Unwrap() error { return e.cause }

// NewLocalCodexRefreshSaveError attaches a recoverable local file write to a
// successful Codex refresh. retry confirms persistence, reports a superseded
// file as (false, nil), or returns an error eligible for bounded rescheduling.
// It must not retain the credential payload or perform a network refresh.
func NewLocalCodexRefreshSaveError(cause error, previousJSON []byte, retry func(context.Context, *Auth) (bool, error)) error {
	previous := parseLocalCodexJSON(previousJSON)
	if cause == nil || retry == nil || previous == nil {
		return cause
	}
	diskDigest, _ := localCodexMetadataDigest(previous.Metadata)
	return &localCodexRefreshSaveError{
		cause:          cause,
		diskDigest:     diskDigest,
		diskCredential: codexFileCredentialDigest(previous),
		retry:          retry,
	}
}

var localCodexSaveBackoff = [...]time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}

// Mutations hold Manager.mu and the per-auth persistence lock. This state keeps
// a stale watcher snapshot fenced even if all disk attempts have completed.
type localCodexRefreshSave struct {
	epoch, credentialVersion uint64
	path                     string
	identity                 string
	diskDigest               [sha256.Size]byte
	diskCredential           [sha256.Size]byte
	priorCredential          [sha256.Size]byte
	retry                    func(context.Context, *Auth) (bool, error)
	failures                 int
	next                     time.Time
}

func localCodexAuthPath(auth *Auth) string {
	if auth == nil || !strings.EqualFold(auth.Provider, "codex") || auth.AuthKind() != AuthKindOAuth ||
		auth.Attributes[AttributeSourceBackend] != AuthSourceFile || auth.Storage != nil || IsPluginVirtualAuth(auth) ||
		auth.Attributes["plugin_id"] != "" || auth.Attributes["compat_name"] != "" {
		return ""
	}
	path := authAttribute(auth, AttributePath)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func parseLocalCodexJSON(raw []byte) *Auth {
	auth := &Auth{}
	if json.Unmarshal(raw, &auth.Metadata) != nil || !strings.EqualFold(authMetadataString(auth, "type"), "codex") {
		return nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	return auth
}

func localCodexMetadataDigest(metadata map[string]any) ([sha256.Size]byte, error) {
	raw, errMarshal := json.Marshal(metadata)
	return sha256.Sum256(raw), errMarshal
}

func codexFileCredentialDigest(auth *Auth) [sha256.Size]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		authMetadataString(auth, "access_token"),
		authMetadataString(auth, "refresh_token"),
		authMetadataString(auth, "id_token"),
		authMetadataString(auth, "account_id"),
		authMetadataString(auth, "pool_account_id"),
	}, "\x00")))
}

func (s *localCodexRefreshSave) matches(auth *Auth) bool {
	return s != nil && auth != nil && auth.RegistrationEpoch == s.epoch &&
		auth.CredentialVersion == s.credentialVersion && localCodexAuthPath(auth) == s.path &&
		AccountIdentity(auth) == s.identity
}

func (s *localCodexRefreshSave) matchesFileSnapshot(auth *Auth) bool {
	return s != nil && auth.RegistrationEpoch == 0 && auth.Metadata != nil &&
		localCodexAuthPath(auth) == s.path
}

func (s *localCodexRefreshSave) isOldCredential(auth *Auth) bool {
	digest := codexFileCredentialDigest(auth)
	return digest == s.diskCredential || digest == s.priorCredential
}

// IsLocalCodexRefreshFileEcho identifies an older token snapshot before
// watcher-driven account-identity reconciliation. Update checks it again
// while holding the persistence lock.
func (m *Manager) IsLocalCodexRefreshFileEcho(ctx context.Context, auth *Auth) bool {
	if m == nil || auth == nil || !shouldSkipPersist(ctx) {
		return false
	}
	pLock := m.authPersistenceLock(auth.ID)
	pLock.mu.Lock()
	defer pLock.mu.Unlock()
	resolved, discard := m.reconcileLocalCodexFileSnapshot(auth)
	return discard || resolved != auth
}

// reconcileLocalCodexFileSnapshot runs with the per-auth persistence lock.
// A live file edit can still change settings while the newer tokens are in RAM.
func (m *Manager) reconcileLocalCodexFileSnapshot(auth *Auth) (resolved *Auth, discard bool) {
	m.mu.RLock()
	save := m.localCodexRefreshSaves[auth.ID]
	current := m.auths[auth.ID]
	if !save.matches(current) || !save.matchesFileSnapshot(auth) {
		m.mu.RUnlock()
		return auth, false
	}
	current = current.Clone()
	m.mu.RUnlock()
	incomingDigest, errDigest := localCodexMetadataDigest(auth.Metadata)
	if errDigest != nil || (incomingDigest == save.diskDigest && save.isOldCredential(auth)) {
		return current, true
	}
	raw, errRead := os.ReadFile(save.path)
	observed := parseLocalCodexJSON(raw)
	if errRead != nil || observed == nil {
		return current, true
	}
	observedDigest, _ := localCodexMetadataDigest(observed.Metadata)
	if observedDigest != incomingDigest {
		return current, true
	}
	if !save.isOldCredential(auth) {
		return auth, false
	}

	resolved = auth.Clone()
	resolved.RegistrationEpoch = current.RegistrationEpoch
	resolved.LastRefreshedAt = current.LastRefreshedAt
	resolved.NextRefreshAfter = current.NextRefreshAfter
	for _, key := range []string{"access_token", "refresh_token", "id_token", "account_id", "email", "expired", "last_refresh"} {
		if value, ok := current.Metadata[key]; ok {
			resolved.Metadata[key] = value
		} else {
			delete(resolved.Metadata, key)
		}
	}
	return resolved, false
}

func (m *Manager) queueLocalCodexRefreshSave(base, updated *Auth, storeRevision uint64, errSave error) {
	var failed *localCodexRefreshSaveError
	if !errors.As(errSave, &failed) || base == nil || !CredentialsChanged(base, updated) {
		return
	}
	path := localCodexAuthPath(updated)
	if path == "" || path != localCodexAuthPath(base) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.storeRevision != storeRevision || !sameCredentialSnapshot(updated, m.auths[updated.ID]) {
		return
	}
	if m.localCodexRefreshSaves == nil {
		m.localCodexRefreshSaves = make(map[string]*localCodexRefreshSave)
	}
	m.localCodexRefreshSaves[updated.ID] = &localCodexRefreshSave{
		epoch:             updated.RegistrationEpoch,
		credentialVersion: updated.CredentialVersion,
		path:              path,
		identity:          AccountIdentity(updated),
		diskDigest:        failed.diskDigest,
		diskCredential:    failed.diskCredential,
		priorCredential:   codexFileCredentialDigest(base),
		retry:             failed.retry,
		next:              time.Now().Add(localCodexSaveBackoff[0]),
	}
	m.wakeLocalCodexRefreshSaves()
}

func (m *Manager) wakeLocalCodexRefreshSaves() {
	select {
	case m.localCodexSaveWake <- struct{}{}:
	default:
	}
}

func (m *Manager) runLocalCodexRefreshSaves(ctx context.Context) {
	for ctx.Err() == nil {
		var dueID string
		var next time.Time
		m.mu.RLock()
		for id, save := range m.localCodexRefreshSaves {
			current := m.auths[id]
			if save.retry == nil || !save.matches(current) || current.Disabled || current.Status == StatusDisabled {
				continue
			}
			if next.IsZero() || save.next.Before(next) {
				dueID, next = id, save.next
			}
		}
		m.mu.RUnlock()
		if next.IsZero() {
			select {
			case <-ctx.Done():
			case <-m.localCodexSaveWake:
			}
			continue
		}
		delay := time.Until(next)
		if delay <= 0 {
			m.retryLocalCodexRefreshSave(ctx, dueID)
			continue
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
		case <-m.localCodexSaveWake:
		case <-timer.C:
		}
		timer.Stop()
	}
}

func (m *Manager) retryLocalCodexRefreshSave(ctx context.Context, id string) {
	m.storeReloadMu.RLock()
	defer m.storeReloadMu.RUnlock()
	pLock := m.authPersistenceLock(id)
	pLock.mu.Lock()
	defer pLock.mu.Unlock()

	m.mu.RLock()
	save := m.localCodexRefreshSaves[id]
	current := m.auths[id]
	if ctx.Err() != nil || !save.matches(current) || save.retry == nil || time.Now().Before(save.next) ||
		current.Disabled || current.Status == StatusDisabled {
		m.mu.RUnlock()
		return
	}
	snapshot := current.Clone()
	m.mu.RUnlock()
	done, errRetry := save.retry(ctx, snapshot)
	if ctx.Err() != nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	logger := log.WithField("auth_id", id)
	if errRetry == nil {
		save.retry = nil
		if done {
			logger.Info("local Codex credential persistence recovered")
		} else {
			logger.Warn("local Codex credential save retry stopped because the source changed")
		}
		return
	}
	save.failures++
	if save.failures == len(localCodexSaveBackoff) {
		save.retry = nil
		logger.Warn("local Codex credential save retries exhausted; the refreshed token may be lost on restart")
		return
	}
	save.next = time.Now().Add(localCodexSaveBackoff[save.failures])
}
