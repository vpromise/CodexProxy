package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestProviderSettingOverridesRoundTrip(t *testing.T) {
	type overrides struct {
		disableCooling *bool
		requestRetry   *int
		headers        map[string]string
	}
	for _, provider := range []struct {
		section, firstFields string
		put, patch           func(*Handler, *gin.Context)
		read                 func(*config.Config) overrides
	}{
		{
			section:     "claude-api-key",
			firstFields: `"api-key":"fixture"`,
			put:         (*Handler).PutClaudeKeys, patch: (*Handler).PatchClaudeKey,
			read: func(cfg *config.Config) overrides {
				key := cfg.ClaudeKey[0]
				return overrides{key.DisableCooling, key.RequestRetry, key.Headers}
			},
		},
		{
			section:     "codex-api-key",
			firstFields: `"api-key":"fixture","base-url":"https://codex.example.com"`,
			put:         (*Handler).PutCodexKeys, patch: (*Handler).PatchCodexKey,
			read: func(cfg *config.Config) overrides {
				key := cfg.CodexKey[0]
				return overrides{key.DisableCooling, key.RequestRetry, key.Headers}
			},
		},
		{
			section:     "openai-compatibility",
			firstFields: `"name":"fixture","base-url":"https://compat.example.com"`,
			put:         (*Handler).PutOpenAICompat, patch: (*Handler).PatchOpenAICompat,
			read: func(cfg *config.Config) overrides {
				key := cfg.OpenAICompatibility[0]
				return overrides{key.DisableCooling, key.RequestRetry, key.Headers}
			},
		},
	} {
		t.Run(provider.section, func(t *testing.T) {
			path := writeTestConfigFile(t)
			h := &Handler{cfg: &config.Config{DisableCooling: true, RequestRetry: 3}, configFilePath: path}
			endpoint := "/v0/management/" + provider.section
			read := func() overrides {
				t.Helper()
				reloaded, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				return provider.read(reloaded)
			}
			request := func(method, body string, invoke func(*Handler, *gin.Context), want int) {
				t.Helper()
				rec := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(rec)
				ctx.Request = httptest.NewRequest(method, endpoint, strings.NewReader(body))
				ctx.Request.Header.Set("Content-Type", "application/json")
				invoke(h, ctx)
				if rec.Code != want {
					t.Fatalf("%s %s: status %d, want %d, body=%s", method, endpoint, rec.Code, want, rec.Body)
				}
			}
			request(http.MethodPut, "[{"+provider.firstFields+
				`,"disable-cooling":false,"request-retry":0,"headers":{"X-Keep":"keep","X-Remove":"old"}}]`,
				provider.put, http.StatusOK)
			if got := read(); got.disableCooling == nil || *got.disableCooling ||
				got.requestRetry == nil || *got.requestRetry != 0 || got.headers["X-Remove"] != "old" {
				t.Fatalf("initial save lost explicit overrides: %+v", got)
			}
			request(http.MethodPut, "null", provider.put, http.StatusBadRequest)
			request(http.MethodPut, `{"items":null}`, provider.put, http.StatusBadRequest)
			if got := read(); got.disableCooling == nil || *got.disableCooling {
				t.Fatalf("invalid replacement changed the credential: %+v", got)
			}
			request(http.MethodPatch, `{"index":0,"value":{"headers":{"X-Keep":"changed"}}}`, provider.patch, http.StatusOK)
			if got := read(); got.disableCooling == nil || *got.disableCooling ||
				got.requestRetry == nil || *got.requestRetry != 0 || len(got.headers) != 1 ||
				got.headers["X-Keep"] != "changed" {
				t.Fatalf("PATCH did not retain omitted values or replace headers: %+v", got)
			}
			request(http.MethodPatch, `{"index":0,"value":{"disable-cooling":null,"request-retry":null,"headers":null}}`,
				provider.patch, http.StatusOK)
			if got := read(); got.disableCooling != nil || got.requestRetry != nil || len(got.headers) != 0 {
				t.Fatalf("explicit nulls did not reset the overrides: %+v", got)
			}
			request(http.MethodPut, `{"items":[]}`, provider.put, http.StatusOK)
			final, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(final.ClaudeKey)+len(final.CodexKey)+len(final.OpenAICompatibility) != 0 {
				t.Fatal("explicit empty list did not clear its section")
			}
		})
	}
}

func TestClaudeNestedKeyOptionsRoundTrip(t *testing.T) {
	path := writeTestConfigFile(t)
	h := &Handler{cfg: &config.Config{}, configFilePath: path}
	request := func(method, body string, invoke func(*Handler, *gin.Context)) *config.CloakConfig {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(method, "/v0/management/claude-api-key", strings.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		invoke(h, ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body=%s", method, rec.Code, rec.Body)
		}
		cfg, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.ClaudeKey[0].Cloak
	}

	initial := request(http.MethodPut,
		`[{"api-key":"fixture","cloak":{"mode":"auto","strict-mode":true,"cache-user-id":false}}]`, (*Handler).PutClaudeKeys)
	if initial == nil || initial.CacheUserID == nil || *initial.CacheUserID || !initial.StrictMode {
		t.Fatalf("initial settings changed: %+v", initial)
	}
	partial := request(http.MethodPatch, `{"index":0,"value":{"cloak":{"strict-mode":false}}}`, (*Handler).PatchClaudeKey)
	if partial == nil || partial.StrictMode || partial.Mode != "auto" || partial.CacheUserID == nil || *partial.CacheUserID {
		t.Fatalf("partial edit did not clear the stored value or preserve omitted fields: %+v", partial)
	}
	replacement := request(http.MethodPut, `[{"api-key":"fixture","cloak":{}}]`, (*Handler).PutClaudeKeys)
	if replacement == nil || replacement.Mode != "" || replacement.StrictMode || replacement.CacheUserID != nil {
		t.Fatalf("full replacement did not preserve an explicit empty object: %+v", replacement)
	}
	if got := request(http.MethodPatch, `{"index":0,"value":{"cloak":null}}`, (*Handler).PatchClaudeKey); got != nil {
		t.Fatalf("explicit null did not remove the optional block: %+v", got)
	}
}

func TestRawYAMLThenProviderPatchKeepsExplicitOverrides(t *testing.T) {
	path := writeTestConfigFile(t)
	h := &Handler{cfg: &config.Config{}, configFilePath: path}
	raw := `# Operator note
disable-cooling: true
claude-api-key:
  - api-key: fixture
    disable-cooling: false
    request-retry: 0
    cloak:
      mode: auto
      cache-user-id: false
`
	store := httptest.NewRecorder()
	put, _ := gin.CreateTestContext(store)
	put.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config/yaml", strings.NewReader(raw))
	h.PutConfigYAML(put)
	if store.Code != http.StatusOK {
		t.Fatalf("raw YAML PUT: status=%d body=%s", store.Code, store.Body)
	}

	edit := httptest.NewRecorder()
	patch, _ := gin.CreateTestContext(edit)
	patch.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"prefix":"fixture-prefix"}}`))
	patch.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(patch)
	if edit.Code != http.StatusOK {
		t.Fatalf("provider PATCH: status=%d body=%s", edit.Code, edit.Body)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	key := cfg.ClaudeKey[0]
	if !cfg.DisableCooling || key.DisableCooling == nil || *key.DisableCooling ||
		key.RequestRetry == nil || *key.RequestRetry != 0 || key.Cloak == nil ||
		key.Cloak.CacheUserID == nil || *key.Cloak.CacheUserID || key.Prefix != "fixture-prefix" {
		t.Fatalf("unrelated edit changed the effective overrides: %+v", key)
	}
}
