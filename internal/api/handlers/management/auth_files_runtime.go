package management

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// authFileRuntime keeps one upload on a consistent configuration and manager
// while hot reload and management writes publish newer runtime references.
type authFileRuntime struct {
	cfg             *config.Config
	manager         *coreauth.Manager
	postPersistHook coreauth.PostAuthHook
}

func (h *Handler) snapshotAuthFileRuntime() authFileRuntime {
	if h == nil {
		return authFileRuntime{cfg: &config.Config{}}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cfg := h.cfg.CloneForRuntime()
	if cfg == nil {
		cfg = &config.Config{}
	}
	return authFileRuntime{cfg: cfg, manager: h.authManager, postPersistHook: h.postAuthPersistHook}
}
