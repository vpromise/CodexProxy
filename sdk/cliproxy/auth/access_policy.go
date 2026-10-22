package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// clientAccessPolicy is independent of executor configuration so a slow runtime
// reload cannot reopen a grant already revoked by a successful management save.
type clientAccessPolicy struct {
	Isolation config.AccessIsolation
	Keys      []string
	Home      bool
	Revision  string
}

func newClientAccessPolicy(cfg *config.Config) *clientAccessPolicy {
	p := &clientAccessPolicy{}
	if cfg != nil {
		p.Home = cfg.Home.Enabled
		p.Keys = slices.Clone(cfg.APIKeys)
		p.Isolation = cfg.AccessIsolation
		p.Isolation.Keys = slices.Clone(cfg.AccessIsolation.Keys)
		p.Isolation.Pools = slices.Clone(cfg.AccessIsolation.Pools)
		for i := range p.Isolation.Pools {
			p.Isolation.Pools[i].Accounts = slices.Clone(p.Isolation.Pools[i].Accounts)
		}
	}
	data, _ := json.Marshal(p)
	digest := sha256.Sum256(data)
	p.Revision = hex.EncodeToString(digest[:])
	return p
}

// PublishAccessPolicy gives the management boundary ownership of client grants.
// Subsequent executor reloads must not publish their possibly older grants.
func (m *Manager) PublishAccessPolicy(cfg *config.Config) {
	if m == nil {
		return
	}
	m.accessPolicyMu.Lock()
	defer m.accessPolicyMu.Unlock()
	m.accessPolicyManaged = true
	m.accessPolicy.Store(newClientAccessPolicy(cfg))
}

func (m *Manager) publishRuntimeAccessPolicy(cfg *config.Config) {
	m.accessPolicyMu.Lock()
	defer m.accessPolicyMu.Unlock()
	if !m.accessPolicyManaged {
		m.accessPolicy.Store(newClientAccessPolicy(cfg))
	}
}
