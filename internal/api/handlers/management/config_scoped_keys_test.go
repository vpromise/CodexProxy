package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestGetConfigOmitsPluginManagementConfig(t *testing.T) {
	enabled := true
	h := &Handler{cfg: &config.Config{Plugins: config.PluginsConfig{
		Enabled: true,
		Configs: map[string]config.PluginInstanceConfig{"sample": {Enabled: &enabled}},
	}}}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)

	h.GetConfig(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("decode response: %v", errUnmarshal)
	}
	if _, exists := response["plugins"]; exists {
		t.Fatalf("management config unexpectedly exposes plugins: %s", rec.Body.String())
	}
}

func TestPatchScopedProviderKeyOptions(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*config.Config)
		patch   func(*Handler, *gin.Context)
		weight  func(*config.Config) *int
		disable func(*config.Config) *bool
		body    string
	}{
		{
			name:    "claude",
			setup:   func(cfg *config.Config) { cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "key"}} },
			patch:   (*Handler).PatchClaudeKey,
			weight:  func(cfg *config.Config) *int { return cfg.ClaudeKey[0].Weight },
			disable: func(cfg *config.Config) *bool { return cfg.ClaudeKey[0].DisableCooling },
		},
		{
			name: "codex",
			setup: func(cfg *config.Config) {
				cfg.CodexKey = []config.CodexKey{{APIKey: "key", BaseURL: "https://codex.example.com"}}
			},
			patch:   (*Handler).PatchCodexKey,
			weight:  func(cfg *config.Config) *int { return cfg.CodexKey[0].Weight },
			disable: func(cfg *config.Config) *bool { return cfg.CodexKey[0].DisableCooling },
		},
		{
			name: "openai compatibility",
			setup: func(cfg *config.Config) {
				cfg.OpenAICompatibility = []config.OpenAICompatibility{{
					Name: "compat", BaseURL: "https://compat.example.com",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "key"}},
				}}
			},
			patch:   (*Handler).PatchOpenAICompat,
			weight:  func(cfg *config.Config) *int { return cfg.OpenAICompatibility[0].APIKeyEntries[0].Weight },
			disable: func(cfg *config.Config) *bool { return cfg.OpenAICompatibility[0].DisableCooling },
			body:    `{"index":0,"value":{"api-key-entries":[{"api-key":"key","weight":7}],"disable-cooling":false}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			test.setup(cfg)
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			body := test.body
			if body == "" {
				body = `{"index":0,"value":{"weight":7,"disable-cooling":false}}`
			}
			ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/key", strings.NewReader(body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.patch(h, ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
			}
			if got := test.weight(cfg); got == nil || *got != 7 {
				t.Fatalf("weight = %v, want 7", got)
			}
			if got := test.disable(cfg); got == nil || *got {
				t.Fatalf("disable-cooling = %v, want false", got)
			}
		})
	}
}

func TestDeleteClaudeKeyWithExplicitEmptyBaseURL(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "shared-key", BaseURL: ""},
			{APIKey: "shared-key", BaseURL: "https://claude.example.com"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/claude-api-key?api-key=shared-key&base-url=", nil)
	h.DeleteClaudeKey(ctx)
	if rec.Code != http.StatusOK || len(h.cfg.ClaudeKey) != 1 || h.cfg.ClaudeKey[0].BaseURL != "https://claude.example.com" {
		t.Fatalf("status=%d keys=%v body=%s", rec.Code, h.cfg.ClaudeKey, rec.Body.String())
	}
}

func TestDeleteCodexKeyRejectsAmbiguousBaseURL(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{CodexKey: []config.CodexKey{
			{APIKey: "shared-key", BaseURL: "https://a.example.com"},
			{APIKey: "shared-key", BaseURL: "https://b.example.com"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/codex-api-key?api-key=shared-key", nil)
	h.DeleteCodexKey(ctx)
	if rec.Code != http.StatusBadRequest || len(h.cfg.CodexKey) != 2 {
		t.Fatalf("status=%d keys=%v body=%s", rec.Code, h.cfg.CodexKey, rec.Body.String())
	}
}
