package access

import (
	"context"
	"net/http"
	"sync"
)

// Manager coordinates authentication providers.
type Manager struct {
	mu             sync.RWMutex
	providers      []Provider
	allowAnonymous bool
}

// NewManager constructs an empty manager.
func NewManager() *Manager {
	return &Manager{}
}

// SetProviders replaces the active provider list.
func (m *Manager) SetProviders(providers []Provider) {
	if m == nil {
		return
	}
	cloned := make([]Provider, len(providers))
	copy(cloned, providers)
	m.mu.Lock()
	m.providers = cloned
	m.mu.Unlock()
}

// Configure atomically replaces the provider list and anonymous-access policy.
// Anonymous access only applies when no providers are configured.
func (m *Manager) Configure(providers []Provider, allowAnonymous bool) {
	if m == nil {
		return
	}
	cloned := make([]Provider, len(providers))
	copy(cloned, providers)
	m.mu.Lock()
	m.providers = cloned
	m.allowAnonymous = allowAnonymous
	m.mu.Unlock()
}

// SetAllowAnonymous updates whether an empty provider list permits requests.
func (m *Manager) SetAllowAnonymous(allow bool) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.allowAnonymous = allow
	m.mu.Unlock()
}

// AllowAnonymous reports whether an empty provider list permits requests.
func (m *Manager) AllowAnonymous() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.allowAnonymous
}

// Providers returns a snapshot of the active providers.
func (m *Manager) Providers() []Provider {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	snapshot := make([]Provider, len(m.providers))
	copy(snapshot, m.providers)
	return snapshot
}

// Authenticate evaluates providers until one succeeds.
func (m *Manager) Authenticate(ctx context.Context, r *http.Request) (*Result, *AuthError) {
	if m == nil {
		return nil, NewNoCredentialsError()
	}
	m.mu.RLock()
	providers := make([]Provider, len(m.providers))
	copy(providers, m.providers)
	allowAnonymous := m.allowAnonymous
	m.mu.RUnlock()
	if len(providers) == 0 {
		if allowAnonymous {
			return nil, nil
		}
		return nil, NewNoCredentialsError()
	}

	var (
		missing bool
		invalid bool
	)

	for _, provider := range providers {
		if provider == nil {
			continue
		}
		res, authErr := provider.Authenticate(ctx, r)
		if authErr == nil {
			return res, nil
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNotHandled) {
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeNoCredentials) {
			missing = true
			continue
		}
		if IsAuthErrorCode(authErr, AuthErrorCodeInvalidCredential) {
			invalid = true
			continue
		}
		return nil, authErr
	}

	if invalid {
		return nil, NewInvalidCredentialError()
	}
	if missing {
		return nil, NewNoCredentialsError()
	}
	return nil, NewNoCredentialsError()
}
