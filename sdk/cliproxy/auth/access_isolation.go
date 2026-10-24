package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"net/http"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type clientIdentityKey struct{}
type accessScopeKey struct{}
type accessCeilingKey struct{}
type clientIdentity struct{ key, provider string }

// AccessScope is an immutable grant captured at a request boundary. Callers may
// retain it only for an in-progress operation, never for the next socket message.
type AccessScope struct {
	enabled   bool
	denied    bool
	provider  string
	pool      string
	members   map[string]bool
	caller    string
	revision  string
	operation uint64
}

// WithClientIdentity must only be called after successful client authentication.
func WithClientIdentity(ctx context.Context, key, provider string) context.Context {
	return context.WithValue(ctx, clientIdentityKey{}, clientIdentity{key, provider})
}

func CopyClientIdentity(ctx, source context.Context) context.Context {
	if source == nil {
		return ctx
	}
	if ceiling, ok := source.Value(accessCeilingKey{}).(*AccessScope); ok {
		ctx = WithAccessCeiling(ctx, ceiling)
	}
	if identity, ok := source.Value(clientIdentityKey{}).(clientIdentity); ok {
		return context.WithValue(ctx, clientIdentityKey{}, identity)
	}
	return ctx
}

func AccessScopeFromContext(ctx context.Context) *AccessScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(accessScopeKey{}).(*AccessScope)
	return scope
}

func (s *AccessScope) Enabled() bool { return s != nil && s.enabled }

// LocalSessionKey namespaces internal session state by the authorized caller.
// This key must never replace a client session identifier in an upstream request.
func (s *AccessScope) LocalSessionKey(sessionID string) string {
	if !s.Enabled() || s.caller == "" || sessionID == "" {
		return sessionID
	}
	return s.caller + ":" + sessionID
}

func (s *AccessScope) Allows(a *Auth) bool {
	return !s.Enabled() || (!s.denied && a != nil && a.Provider == s.provider && s.members[AccountIdentity(a)])
}
func (s *AccessScope) Error() error {
	if s != nil && s.denied {
		return &Error{Code: "access_denied", Message: "Client key is not authorized for an account pool", HTTPStatus: http.StatusForbidden}
	}
	return nil
}

// NewAccessRequest captures the current policy, including for previously opened sockets.
func (m *Manager) NewAccessRequest(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	scope := &AccessScope{operation: accessOperationSequence.Add(1)}
	identity, _ := ctx.Value(clientIdentityKey{}).(clientIdentity)
	if identity.key != "" {
		digest := sha256.Sum256([]byte(identity.provider + "\x00" + identity.key))
		scope.caller = hex.EncodeToString(digest[:])
	}
	if m != nil {
		policy := m.accessPolicy.Load()
		if policy != nil {
			scope.revision = policy.Revision
		}
		if policy != nil && policy.Isolation.Enabled {
			scope.enabled, scope.denied = true, true
			if !policy.Home && identity.provider == "config-inline" && slices.Contains(policy.Keys, identity.key) {
				for _, binding := range policy.Isolation.Keys {
					if binding.Key != identity.key || binding.Pool == "" {
						continue
					}
					for _, pool := range policy.Isolation.Pools {
						if pool.ID != binding.Pool || (pool.Provider != "codex" && pool.Provider != "claude") {
							continue
						}
						scope.provider, scope.pool, scope.denied = pool.Provider, pool.ID, false
						scope.members = make(map[string]bool, len(pool.Accounts))
						for _, id := range pool.Accounts {
							scope.members[id] = true
						}
					}
				}
			}
		}
	}
	if ceiling, ok := ctx.Value(accessCeilingKey{}).(*AccessScope); ok && ceiling != nil && (ceiling.enabled || scope.enabled) {
		if !ceiling.enabled || !scope.enabled || scope.pool != ceiling.pool || ceiling.denied {
			scope.enabled, scope.denied = true, true
		} else {
			for id := range scope.members {
				if !ceiling.members[id] {
					delete(scope.members, id)
				}
			}
		}
	}
	ctx = context.WithValue(ctx, accessScopeKey{}, scope)
	if scope.denied {
		auditAccessDecision(ctx, nil, "denied", "client_grant_unavailable")
	}
	return ctx
}

func (m *Manager) AuthorizeRequest(ctx context.Context) (context.Context, error) {
	if AccessScopeFromContext(ctx) == nil {
		ctx = m.NewAccessRequest(ctx)
	}
	return ctx, AccessScopeFromContext(ctx).Error()
}

// AuthorizedModels lists registered models without dropping temporarily cooled
// down credentials. Authorization and current capacity are separate concerns.
func (m *Manager) AuthorizedModels(ctx context.Context) []*registry.ModelInfo {
	scope := AccessScopeFromContext(ctx)
	result := []*registry.ModelInfo{}
	seen := map[string]bool{}
	for _, a := range m.List() {
		if !scope.Allows(a) {
			continue
		}
		for _, model := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
			if model != nil && !seen[model.ID] {
				seen[model.ID] = true
				result = append(result, model)
			}
		}
	}
	return result
}

// IsolationEnabled reports whether native pool authorization is required.
func (m *Manager) IsolationEnabled() bool {
	if m == nil {
		return false
	}
	policy := m.accessPolicy.Load()
	return policy != nil && policy.Isolation.Enabled
}

// WithAccessCeiling binds a derived credential to its issuance grant. Current
// policy may narrow the grant but never expand it or move it to another pool.
func WithAccessCeiling(ctx context.Context, scope *AccessScope) context.Context {
	if scope == nil {
		scope = &AccessScope{}
	}
	return context.WithValue(ctx, accessCeilingKey{}, scope)
}

// CheckCurrentAccount authorizes a new operation on an existing native session.
func (m *Manager) CheckCurrentAccount(ctx context.Context, selected *Auth) (errAccess error) {
	ctx = m.NewAccessRequest(ctx)
	defer func() {
		if errAccess != nil {
			auditAccessDecision(ctx, nil, "denied", "session_account_unavailable")
		} else {
			auditAccessDecision(ctx, selected, "session_authorized", "current_grant")
		}
	}()
	scope := AccessScopeFromContext(ctx)
	if err := scope.Error(); err != nil {
		return err
	}
	if !scope.Enabled() {
		return nil
	}
	if selected == nil {
		return &Error{Code: "access_denied", Message: "Session account is unavailable", HTTPStatus: http.StatusForbidden}
	}
	current, ok := m.GetByID(selected.ID)
	if !ok || current.Disabled || AccountIdentity(current) != AccountIdentity(selected) || !scope.Allows(current) {
		return &Error{Code: "access_denied", Message: "Session account is no longer authorized", HTTPStatus: http.StatusForbidden}
	}
	return nil
}

const poolCallerMetadataKey = "account_pool_caller"

func scopeAccessOptions(ctx context.Context, opts coreexecutor.Options) coreexecutor.Options {
	scope := AccessScopeFromContext(ctx)
	if !scope.Enabled() {
		return opts
	}
	opts.Metadata = maps.Clone(opts.Metadata)
	opts.EnsureMetadata()
	opts.Metadata[poolCallerMetadataKey] = scope.caller
	opts.Metadata[coreexecutor.CallerScopeMetadataKey] = scope.caller
	return opts
}

// ValidateAccessSelection checks a request before local prewarming or native execution.
func (m *Manager) ValidateAccessSelection(ctx context.Context, providers []string, model string, opts coreexecutor.Options) (errAccess error) {
	defer func() {
		if errAccess != nil {
			auditAccessDecision(ctx, nil, "denied", "selection_not_authorized_or_available")
		}
	}()
	scope := AccessScopeFromContext(ctx)
	if !scope.Enabled() {
		return nil
	}
	if err := scope.Error(); err != nil {
		return err
	}
	denied := &Error{Code: "access_denied", Message: "Requested provider, model or account is outside the authorized pool", HTTPStatus: http.StatusForbidden}
	if !slices.Contains(m.normalizeProviders(providers), scope.provider) {
		return denied
	}
	if pin := pinnedAuthIDFromMetadata(opts.Metadata); pin != "" {
		account, ok := m.GetByID(pin)
		if !ok || !scope.Allows(account) {
			return denied
		}
	}
	hasMember := false
	for _, account := range m.List() {
		if scope.Allows(account) {
			hasMember = true
			break
		}
	}
	if !hasMember {
		return &Error{Code: "pool_unavailable", Message: "No authorized accounts are available in the account pool", HTTPStatus: http.StatusServiceUnavailable}
	}
	if model != "" {
		hasMember := false
		for _, account := range m.List() {
			if !scope.Allows(account) {
				continue
			}
			hasMember = true
			if m.authSupportsRouteModel(registry.GetGlobalRegistry(), account, model) {
				return nil
			}
		}
		if hasMember {
			return denied
		}
	}
	return nil
}
