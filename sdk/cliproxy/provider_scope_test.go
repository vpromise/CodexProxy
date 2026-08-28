package cliproxy

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestSupportsAuthScopedProviders(t *testing.T) {
	service := &Service{coreManager: coreauth.NewManager(nil, nil, nil)}
	tests := []struct {
		name string
		auth *coreauth.Auth
		want bool
	}{
		{name: "codex", auth: &coreauth.Auth{Provider: "codex"}, want: true},
		{name: "claude", auth: &coreauth.Auth{Provider: "claude"}, want: true},
		{name: "openai compatibility", auth: &coreauth.Auth{Provider: "openai-compatible-example"}, want: true},
		{name: "compatibility metadata wins", auth: openAICompatKimiAuth(), want: true},
		{name: "custom compatible base URL", auth: &coreauth.Auth{Provider: "custom", Attributes: map[string]string{"base_url": "https://example.invalid/v1"}}, want: true},
		{name: "gemini", auth: &coreauth.Auth{Provider: "gemini"}, want: false},
		{name: "interactions", auth: &coreauth.Auth{Provider: "gemini-interactions"}, want: false},
		{name: "vertex", auth: &coreauth.Auth{Provider: "vertex"}, want: false},
		{name: "aistudio", auth: &coreauth.Auth{Provider: "aistudio"}, want: false},
		{name: "antigravity", auth: &coreauth.Auth{Provider: "antigravity"}, want: false},
		{name: "kimi native", auth: &coreauth.Auth{Provider: "kimi"}, want: false},
		{name: "xai", auth: &coreauth.Auth{Provider: "xai"}, want: false},
		{name: "unknown without extension", auth: &coreauth.Auth{Provider: "unknown"}, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := service.supportsAuth(test.auth); got != test.want {
				t.Fatalf("supportsAuth() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPruneUnsupportedRuntimeAuthsKeepsSourceRecordsOutOfScope(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	ctx := coreauth.WithSkipPersist(context.Background())
	for _, auth := range []*coreauth.Auth{
		{ID: "codex-auth", Provider: "codex"},
		{ID: "claude-auth", Provider: "claude"},
		{ID: "gemini-auth", Provider: "gemini"},
		{ID: "xai-auth", Provider: "xai"},
	} {
		if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}
	service := &Service{coreManager: manager}
	if removed := service.pruneUnsupportedRuntimeAuths(ctx); removed != 2 {
		t.Fatalf("removed = %d, want 2", removed)
	}
	for _, id := range []string{"codex-auth", "claude-auth"} {
		if _, ok := manager.GetByID(id); !ok {
			t.Fatalf("expected %s to remain", id)
		}
	}
	for _, id := range []string{"gemini-auth", "xai-auth"} {
		if _, ok := manager.GetByID(id); ok {
			t.Fatalf("expected %s to be removed from runtime", id)
		}
	}
}

func TestRegisterConfigAPIKeyAuthsRegistersScopedProviders(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{APIKey: "claude-key"}},
		CodexKey:  []config.CodexKey{{APIKey: "codex-key"}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "example",
			BaseURL: "https://example.invalid/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
				APIKey: "compatible-key",
			}},
		}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}
	service.registerConfigAPIKeyAuths(coreauth.WithSkipPersist(context.Background()), cfg)

	providers := make(map[string]bool)
	for _, auth := range manager.List() {
		providers[auth.Provider] = true
	}
	for _, provider := range []string{"claude", "codex", "openai-compatible-example"} {
		if !providers[provider] {
			t.Fatalf("expected provider %s, got %v", provider, providers)
		}
	}
}
