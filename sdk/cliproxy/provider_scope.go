package cliproxy

import (
	"context"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func isScopedCoreProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "codex", "claude", "openai", "openai-compatibility":
		return true
	default:
		return strings.HasPrefix(provider, "openai-compatible-")
	}
}

// supportsAuth reports whether an auth belongs to the scoped runtime surface.
// Explicit OpenAI-compatible metadata wins over a provider-shaped display name,
// allowing compatible gateways without restoring their removed native adapters.
func (s *Service) supportsAuth(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	if _, _, compatible := openAICompatInfoFromAuth(auth); compatible {
		return true
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if isScopedCoreProvider(provider) {
		return true
	}
	if provider == "" {
		return false
	}
	if s != nil {
		s.cfgMu.RLock()
		cfg := s.cfg
		s.cfgMu.RUnlock()
		if s.hasNativeOpenAICompatExecutorConfig(auth, provider, cfg) {
			return true
		}
	}
	if s != nil && s.pluginHost != nil {
		if s.pluginHost.HasExecutorCandidateProvider(provider) || pluginHostHasAuthProvider(s.pluginHost, provider) {
			return true
		}
	}
	if s != nil && s.coreManager != nil {
		_, registered := s.coreManager.Executor(provider)
		return registered
	}
	return false
}

// pruneUnsupportedRuntimeAuths removes out-of-scope credentials from in-memory
// routing without deleting their source files or token-store records.
func (s *Service) pruneUnsupportedRuntimeAuths(ctx context.Context) int {
	if s == nil || s.coreManager == nil {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	removed := 0
	for _, auth := range s.coreManager.List() {
		if s.supportsAuth(auth) {
			continue
		}
		GlobalModelRegistry().UnregisterClient(auth.ID)
		s.coreManager.Remove(ctx, auth.ID)
		removed++
	}
	if removed > 0 {
		log.WithField("count", removed).Info("ignored credentials for providers outside the scoped runtime")
	}
	return removed
}
