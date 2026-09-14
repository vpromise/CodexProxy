package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// SetRetryConfig updates additional credential retry rounds, the per-round credential limit, and the cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration, maxRetryCredentials int) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryCredentials < 0 {
		maxRetryCredentials = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryCredentials.Store(int32(maxRetryCredentials))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	provider := strings.TrimSpace(executor.Identifier())
	if provider == "" {
		return
	}

	var replaced ProviderExecutor
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	m.mu.Unlock()

	if replaced == nil || replaced == executor {
		return
	}
	if closer, ok := replaced.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("register auth: %w", errWeight)
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	now := time.Now()
	if auth.Generation == 0 {
		auth.Generation = 1
	}
	if auth.CreatedAt.IsZero() {
		auth.CreatedAt = now
	}
	auth.UpdatedAt = now
	cooldownStateChanged := normalizeModelStates(auth)
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	pLock := m.authPersistenceLock(auth.ID)
	pLock.mu.Lock()
	m.mu.Lock()
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing, exists := m.auths[auth.ID]; exists && existing != nil && existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	m.authEpochs[auth.ID]++
	auth.RegistrationEpoch = m.authEpochs[auth.ID]
	auth.Generation = 1
	auth.availabilityEpoch = 1
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone.Clone()
	m.mu.Unlock()
	errPersist := m.persistAuthLocked(ctx, authClone, pLock)
	pLock.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone.Clone())
	}
	m.publishAuthModelStates(authClone)
	m.refreshLocalCredentialAdmission(authClone)
	m.queueRefreshReschedule(auth.ID)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return auth.Clone(), errPersist
}

type updateAuthMode int

const (
	updateModeReplace updateAuthMode = iota
	updateModeRefresh
	updateModePrepare
	updateModePatch
)

// UpdatePreparedAuth atomically merges request preparation results into the latest runtime auth
// under the manager lock, preserving concurrent modifications without modifying refresh lifecycle fields.
func (m *Manager) UpdatePreparedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	return m.updateInternal(ctx, base, updated, updateModePrepare)
}

// UpdateRefreshedAuth atomically merges refresh results into the latest runtime auth
// under the manager lock, preserving concurrent modifications (proxy_url, notes, weights, etc.).
func (m *Manager) UpdateRefreshedAuth(ctx context.Context, base, updated *Auth) (*Auth, error) {
	return m.updateInternal(ctx, base, updated, updateModeRefresh)
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	return m.updateInternal(ctx, nil, auth, updateModeReplace)
}

// PatchAuth applies a management mutation to the latest registration atomically.
// The callback must only modify its private auth snapshot; it must not perform
// I/O or call Manager methods. An explicit epoch prevents patching a replacement.
func (m *Manager) PatchAuth(ctx context.Context, id string, epoch uint64, patch func(*Auth) error) (*Auth, error) {
	if patch == nil {
		return nil, fmt.Errorf("patch auth: missing mutation")
	}
	return m.updateInternal(ctx, nil, &Auth{ID: id, RegistrationEpoch: epoch}, updateModePatch, patch)
}

func (m *Manager) updateInternal(ctx context.Context, base, auth *Auth, mode updateAuthMode, patches ...func(*Auth) error) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	NormalizeCredentialMetadata(auth.Metadata)
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return nil, fmt.Errorf("update auth: %w", errWeight)
	}
	pLock := m.authPersistenceLock(auth.ID)
	pLock.mu.Lock()
	m.mu.Lock()
	existing, ok := m.auths[auth.ID]
	if !ok || existing == nil {
		m.mu.Unlock()
		pLock.mu.Unlock()
		return nil, nil
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[auth.ID] {
		m.authEpochs[auth.ID] = existing.RegistrationEpoch
	}
	if mode == updateModePatch {
		if auth.RegistrationEpoch != 0 && auth.RegistrationEpoch != existing.RegistrationEpoch {
			m.mu.Unlock()
			pLock.mu.Unlock()
			return nil, fmt.Errorf("patch auth: registration changed")
		}
		auth = existing.Clone()
		if errPatch := patches[0](auth); errPatch != nil {
			m.mu.Unlock()
			pLock.mu.Unlock()
			return nil, errPatch
		}
		if auth.ID != existing.ID {
			m.mu.Unlock()
			pLock.mu.Unlock()
			return nil, fmt.Errorf("patch auth: cannot change credential ID")
		}
		NormalizeCredentialMetadata(auth.Metadata)
		if errWeight := ValidateAuthWeight(auth); errWeight != nil {
			m.mu.Unlock()
			pLock.mu.Unlock()
			return nil, fmt.Errorf("patch auth: %w", errWeight)
		}
	}
	if (mode == updateModeRefresh || mode == updateModePrepare) && (base == nil || base.ID != auth.ID || existing.RegistrationEpoch != base.RegistrationEpoch) {
		m.mu.Unlock()
		pLock.mu.Unlock()
		return nil, fmt.Errorf("update auth %s: stale registration epoch", auth.ID)
	}
	if mode == updateModeRefresh {
		merged := MergeRefreshedAuth(base, existing, auth)
		if merged != nil {
			auth = merged
			NormalizeCredentialMetadata(auth.Metadata)
		}
	} else if mode == updateModePrepare {
		merged := MergePreparedAuth(base, existing, auth)
		if merged != nil {
			auth = merged
			NormalizeCredentialMetadata(auth.Metadata)
		}
	}
	if auth.RegistrationEpoch != 0 && auth.RegistrationEpoch != existing.RegistrationEpoch {
		m.mu.Unlock()
		pLock.mu.Unlock()
		return nil, fmt.Errorf("update auth %s: stale registration epoch", auth.ID)
	}
	auth.RegistrationEpoch = existing.RegistrationEpoch
	if !auth.indexAssigned && auth.Index == "" {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	auth.Success = existing.Success
	auth.Failed = existing.Failed
	auth.recentRequests = existing.recentRequests
	auth.Generation = existing.Generation + 1
	auth.availabilityEpoch = existing.availabilityEpoch
	if (existing.Disabled || existing.Status == StatusDisabled) != (auth.Disabled || auth.Status == StatusDisabled) {
		auth.availabilityEpoch++
	}
	now := time.Now()
	cooldownStateChanged := false
	if !existing.Disabled && existing.Status != StatusDisabled && !auth.Disabled && auth.Status != StatusDisabled {
		if mode == updateModeReplace && len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
			auth.ModelStates = cloneModelStates(existing.ModelStates)
		}
		if mode == updateModeReplace {
			preserveIncomingAuthCooldown(existing, auth, now)
		}
		if CredentialsChanged(existing, auth) {
			cooldownStateChanged = clearUnauthorizedAuthState(auth, now)
		}
	}
	auth.UpdatedAt = now
	cooldownStateChanged = normalizeModelStates(auth) || cooldownStateChanged
	if m.cooldownDisabledForAuth(auth) || auth.Disabled || auth.Status == StatusDisabled {
		cooldownStateChanged = clearCooldownStateForAuth(auth, now) || cooldownStateChanged
	}
	auth.EnsureIndex()
	authClone := auth.Clone()
	m.auths[auth.ID] = authClone.Clone()
	m.mu.Unlock()
	errPersist := m.persistAuthLocked(ctx, authClone, pLock)
	pLock.mu.Unlock()
	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone.Clone())
	}
	m.publishAuthModelStates(authClone)
	m.refreshLocalCredentialAdmission(authClone)
	m.queueRefreshReschedule(auth.ID)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	if cooldownStateChanged {
		m.persistCooldownStates(context.Background())
	}
	return auth.Clone(), errPersist
}

// Remove deletes an auth from runtime state without persisting.
// Disk and token-store deletion must be handled by the caller.
func (m *Manager) Remove(ctx context.Context, id string) {
	_ = m.RemoveWithPersistence(ctx, id, nil)
}

// RemoveWithPersistence drains earlier writes, removes persisted data, then
// removes runtime state before a replacement registration can be installed.
// The callback may access storage, but must not call Manager mutation methods.
// An optional expected epoch makes delayed removal notifications conditional.
func (m *Manager) RemoveWithPersistence(ctx context.Context, id string, remove func() error, expectedEpoch ...uint64) error {
	if m == nil {
		return nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("remove auth: empty credential ID")
	}
	_ = ctx

	pLock := m.authPersistenceLock(id)
	pLock.mu.Lock()
	if len(expectedEpoch) > 0 && expectedEpoch[0] != 0 {
		m.mu.RLock()
		current := m.auths[id]
		stale := current == nil || current.RegistrationEpoch != expectedEpoch[0]
		m.mu.RUnlock()
		if stale {
			pLock.mu.Unlock()
			return nil
		}
	}
	if remove != nil {
		if errRemove := remove(); errRemove != nil {
			pLock.mu.Unlock()
			return errRemove
		}
	}
	m.mu.Lock()
	existing := m.auths[id]
	if existing == nil {
		m.mu.Unlock()
		pLock.mu.Unlock()
		return nil
	}
	provider := strings.TrimSpace(existing.Provider)
	delete(m.auths, id)
	registry.GetGlobalRegistry().UnregisterClient(id)
	if m.modelPoolOffsets != nil {
		delete(m.modelPoolOffsets, id)
	}
	for sessionID, sessionAuths := range m.homeRuntimeAuths {
		if sessionAuths == nil {
			continue
		}
		delete(sessionAuths, id)
		if len(sessionAuths) == 0 {
			delete(m.homeRuntimeAuths, sessionID)
		}
	}
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64)
	}
	if existing.RegistrationEpoch > m.authEpochs[id] {
		m.authEpochs[id] = existing.RegistrationEpoch
	}
	m.authEpochs[id]++
	tombstoneEpoch := m.authEpochs[id]
	pLock.lastEpoch = tombstoneEpoch
	pLock.lastGeneration = 0
	m.mu.Unlock()

	if !shouldDeferAPIKeyModelAliasRebuild(ctx) {
		m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	}
	if m.scheduler != nil {
		m.scheduler.RecordRemovalTombstone(id, tombstoneEpoch)
	}
	m.queueRefreshUnschedule(id)
	m.invalidateSessionAffinity(id)
	m.removeLocalCredentialAdmission(id)

	if provider != "" {
		if exec, ok := m.Executor(provider); ok && exec != nil {
			if closer, okCloser := exec.(ExecutionSessionCloser); okCloser {
				closer.CloseExecutionSession(CloseAllExecutionSessionsID)
			}
		}
	}
	pLock.mu.Unlock()
	m.persistCooldownStates(context.Background())
	return nil
}

func (m *Manager) invalidateSessionAffinity(authID string) {
	if m == nil || authID == "" {
		return
	}
	if invalidator, ok := m.selector.(interface{ InvalidateAuth(string) }); ok && invalidator != nil {
		invalidator.InvalidateAuth(authID)
	}
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	if m.store == nil {
		m.mu.Unlock()
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	previousAuths := m.auths
	m.auths = make(map[string]*Auth, len(items))
	if m.authEpochs == nil {
		m.authEpochs = make(map[string]uint64, len(items))
	}
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		NormalizeCredentialMetadata(auth.Metadata)
		if errWeight := ValidateAuthWeight(auth); errWeight != nil {
			continue
		}
		auth.EnsureIndex()
		m.authEpochs[auth.ID]++
		auth.RegistrationEpoch = m.authEpochs[auth.ID]
		auth.Generation = 1
		auth.availabilityEpoch = 1
		m.auths[auth.ID] = auth.Clone()
	}

	type removalTombstone struct {
		id    string
		epoch uint64
	}
	var removedTombstones []removalTombstone
	for prevID := range previousAuths {
		if _, exists := m.auths[prevID]; !exists {
			m.authEpochs[prevID]++
			removedTombstones = append(removedTombstones, removalTombstone{
				id:    prevID,
				epoch: m.authEpochs[prevID],
			})
		}
	}

	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.rebuildAPIKeyModelAliasLocked(cfg)
	m.mu.Unlock()

	if m.scheduler != nil {
		for _, rt := range removedTombstones {
			m.scheduler.RecordRemovalTombstone(rt.id, rt.epoch)
		}
	}
	m.syncScheduler()
	return nil
}

type authPersistLock struct {
	mu             sync.Mutex
	lastEpoch      uint64
	lastGeneration uint64
}

// authPersistenceLock is acquired before m.mu when both are needed. Hooks and
// scheduler/registry callbacks run after it is released.
func (m *Manager) authPersistenceLock(id string) *authPersistLock {
	value, _ := m.persistLocks.LoadOrStore(id, &authPersistLock{})
	return value.(*authPersistLock)
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if auth == nil {
		return nil
	}
	pLock := m.authPersistenceLock(auth.ID)
	pLock.mu.Lock()
	defer pLock.mu.Unlock()
	return m.persistAuthLocked(ctx, auth, pLock)
}

// persistAuthLocked orders writes and rejects snapshots of removed/replaced
// registrations. It never calls Store.Save with the manager mutex held.
func (m *Manager) persistAuthLocked(ctx context.Context, auth *Auth, pLock *authPersistLock) error {
	if errWeight := ValidateAuthWeight(auth); errWeight != nil {
		return fmt.Errorf("persist auth: %w", errWeight)
	}
	m.mu.RLock()
	current := m.auths[auth.ID]
	stale := auth.RegistrationEpoch != 0 && (current == nil || current.RegistrationEpoch != auth.RegistrationEpoch)
	if !stale && auth.RegistrationEpoch != 0 && current.Generation > auth.Generation {
		// Refresh bookkeeping can advance the generation without saving. Persist
		// the latest state of this registration instead of losing a durable update.
		auth = current.Clone()
	}
	store := m.store
	m.mu.RUnlock()
	if stale || auth.RegistrationEpoch < pLock.lastEpoch || (auth.RegistrationEpoch == pLock.lastEpoch && auth.Generation < pLock.lastGeneration) {
		return nil
	}
	pLock.lastEpoch = auth.RegistrationEpoch
	pLock.lastGeneration = auth.Generation
	if store == nil || shouldSkipPersist(ctx) || IsConfigAPIKeyAuth(auth) || IsPluginVirtualAuth(auth) || auth.Metadata == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true") {
		return nil
	}
	// Stores may annotate metadata and paths. Keep their mutations isolated from
	// the auth currently being read by request selection and refresh workers.
	_, errSave := store.Save(ctx, auth.Clone())
	return errSave
}
