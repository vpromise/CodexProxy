package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSetConfigAPIKeyExcludedAll(t *testing.T) {
	if got := setConfigAPIKeyExcludedAll([]string{"model-a"}, true); len(got) != 2 || got[1] != "*" {
		t.Fatalf("disable result = %#v", got)
	}
	if got := setConfigAPIKeyExcludedAll([]string{"model-a", "*"}, false); len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("enable result = %#v", got)
	}
}

func TestToggleConfigAPIKeyExcludedAllScopedProviders(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *config.Config
		auth     *coreauth.Auth
		excluded func(*config.Config) []string
	}{
		{
			name:     "codex",
			cfg:      &config.Config{CodexKey: []config.CodexKey{{APIKey: "codex-key", BaseURL: "https://codex.example.com"}}},
			auth:     &coreauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "codex-key"}},
			excluded: func(cfg *config.Config) []string { return cfg.CodexKey[0].ExcludedModels },
		},
		{
			name:     "claude",
			cfg:      &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "claude-key", BaseURL: "https://claude.example.com"}}},
			auth:     &coreauth.Auth{Provider: "claude", Attributes: map[string]string{"api_key": "claude-key"}},
			excluded: func(cfg *config.Config) []string { return cfg.ClaudeKey[0].ExcludedModels },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			idGen := synthesizer.NewStableIDGenerator()
			switch test.auth.Provider {
			case "codex":
				entry := test.cfg.CodexKey[0]
				test.auth.ID, _ = idGen.Next("codex:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, config.FormatSortedHeaders(entry.Headers))
			case "claude":
				entry := test.cfg.ClaudeKey[0]
				test.auth.ID, _ = idGen.Next("claude:apikey", entry.APIKey, entry.BaseURL, entry.ProxyURL, entry.Prefix, config.FormatSortedHeaders(entry.Headers))
			}
			if test.auth.Attributes == nil {
				test.auth.Attributes = make(map[string]string)
			}
			test.auth.Attributes[coreauth.AttributeAuthKind] = coreauth.AuthKindAPIKey
			test.auth.Attributes[coreauth.AttributeSourceBackend] = coreauth.AuthSourceConfig
			test.auth.Attributes[coreauth.AttributeConfigIndex] = "0"
			handled, errToggle := toggleConfigAPIKeyExcludedAll(test.cfg, test.auth, true)
			if errToggle != nil || !handled {
				t.Fatalf("toggle: handled=%t err=%v", handled, errToggle)
			}
			if got := test.excluded(test.cfg); len(got) != 1 || got[0] != "*" {
				t.Fatalf("excluded-models = %#v", got)
			}
		})
	}
}
