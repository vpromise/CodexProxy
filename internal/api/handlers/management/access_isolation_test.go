package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestAccountPoolInventoryMatchesProviderCredentialMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if errWrite := os.WriteFile(path, []byte("{}\n"), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CodexKey = []config.CodexKey{
		{APIKey: "synthetic-codex-account-one", Prefix: "codex-one", PoolAccountID: "record-codex-one"},
		{APIKey: "synthetic-codex-account-two", Prefix: "codex-two", PoolAccountID: "record-codex-two"},
	}
	cfg.ClaudeKey = []config.ClaudeKey{
		{APIKey: "synthetic-claude-account-three", Prefix: "claude-three", PoolAccountID: "record-claude-three"},
	}
	for i, key := range []string{"X", "XY", "XYZ", "WXYZ"} {
		cfg.ClaudeKey = append(cfg.ClaudeKey, config.ClaudeKey{
			APIKey: key, Prefix: "short-" + strings.Repeat("x", i+1), PoolAccountID: "record-" + key,
		})
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if errWrite := os.WriteFile(path, data, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, errLoad := config.LoadConfig(path); errLoad != nil {
		t.Fatal(errLoad)
	}
	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatal(err)
	}
	oauth := &coreauth.Auth{
		ID: "synthetic-oauth", Provider: "codex", Label: "Synthetic OAuth account",
		Metadata: map[string]any{"account_id": "synthetic-oauth-subject", "access_token": "synthetic-oauth-access-token"},
	}
	auths = append(auths, oauth)
	manager := coreauth.NewManager(nil, nil, nil)
	for _, account := range auths {
		if _, errRegister := manager.Register(context.Background(), account); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}
	call := func(handler gin.HandlerFunc) []byte {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		handler(c)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET failed: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	var inventory struct {
		Accounts []struct {
			ID            string `json:"id"`
			Label         string `json:"label"`
			AuthIndex     string `json:"auth_index"`
			Prefix        string `json:"prefix"`
			APIKeyPreview string `json:"api_key_preview"`
		} `json:"accounts"`
	}
	response := call(h.GetAccessIsolation)
	if errDecode := json.Unmarshal(response, &inventory); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(inventory.Accounts) != len(auths) {
		t.Fatalf("got %d accounts, want %d", len(inventory.Accounts), len(auths))
	}
	expectedPreview := map[string]string{
		"codex-one": "sy******ne", "codex-two": "sy******wo", "claude-three": "sy******ee",
		"short-x": "**********", "short-xx": "**********", "short-xxx": "**********", "short-xxxx": "**********",
	}
	for _, account := range inventory.Accounts {
		if account.ID == coreauth.AccountIdentity(oauth) {
			if account.Label != oauth.Label || account.APIKeyPreview != "" {
				t.Fatalf("OAuth display changed: %+v", account)
			}
			continue
		}
		if account.AuthIndex == "" || account.APIKeyPreview != expectedPreview[account.Prefix] || expectedPreview[account.Prefix] == "" {
			t.Fatalf("missing or incorrect credential display: %+v", account)
		}
	}
	// Provider and isolation views must expose the same stable index for each credential.
	for _, handler := range []gin.HandlerFunc{h.GetCodexKeys, h.GetClaudeKeys} {
		var providers map[string][]struct {
			AuthIndex string `json:"auth-index"`
			Prefix    string `json:"prefix"`
		}
		if errDecode := json.Unmarshal(call(handler), &providers); errDecode != nil {
			t.Fatal(errDecode)
		}
		for _, entries := range providers {
			for _, entry := range entries {
				matched := false
				for _, account := range inventory.Accounts {
					if account.Prefix == entry.Prefix && account.AuthIndex == entry.AuthIndex && entry.AuthIndex != "" {
						matched = true
					}
				}
				if !matched {
					t.Fatalf("provider credential has no matching pool metadata: %+v", entry)
				}
			}
		}
	}
	for _, account := range auths {
		if key := account.Attributes["api_key"]; key != "" && bytes.Contains(response, []byte(key)) {
			t.Fatalf("pool response leaked an upstream API key of length %d", len(key))
		}
	}
	if bytes.Contains(response, []byte("synthetic-oauth-access-token")) {
		t.Fatal("pool response leaked an OAuth access token")
	}
}

func TestAccountPoolsPersistAndRejectForeignMembers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("api-keys: [synthetic-client]\nrequest-retry: 7\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	for _, a := range []*coreauth.Auth{
		{ID: "codex-file.json", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-codex"}},
		{ID: "claude-file.json", Provider: "claude", Metadata: map[string]any{"account_uuid": "synthetic-claude", "organization_uuid": "synthetic-org"}},
	} {
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}
	call := func(method string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(method, "/access-isolation", bytes.NewReader(data))
		ctx.Request.Header.Set("Content-Type", "application/json")
		if method == http.MethodGet {
			h.GetAccessIsolation(ctx)
		} else {
			h.PutAccessIsolation(ctx)
		}
		return rec
	}
	var view struct {
		Revision string `json:"revision"`
		Accounts []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(call(http.MethodGet, nil).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, account := range view.Accounts {
		ids[account.Provider] = account.ID
	}
	if ids["codex"] == "" || ids["claude"] == "" || ids["codex"] == "codex-file.json" {
		t.Fatalf("invalid persistent identities: %+v", view)
	}
	body := map[string]any{"revision": view.Revision, "settings": map[string]any{"pools": []any{
		map[string]any{"id": "pool-one", "name": "One", "provider": "codex", "accounts": []string{ids["codex"]}},
		map[string]any{"id": "pool-two", "name": "Two", "provider": "codex", "accounts": []string{ids["codex"]}},
		map[string]any{"id": "empty", "name": "Empty", "provider": "claude", "accounts": []string{}},
	}}}
	if rec := call(http.MethodPut, body); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodPut, body); rec.Code != 409 {
		t.Fatalf("stale write: %d %s", rec.Code, rec.Body.String())
	}
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RequestRetry != 7 || reloaded.AccessIsolation.Enabled || len(reloaded.AccessIsolation.Pools) != 3 {
		t.Fatalf("persistence lost settings")
	}
	h.cfg = reloaded
	if err := json.Unmarshal(call(http.MethodGet, nil).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	body["revision"] = view.Revision
	body["settings"] = map[string]any{"pools": []any{map[string]any{"id": "bad", "name": "Bad", "provider": "codex", "accounts": []string{ids["claude"]}}}}
	if rec := call(http.MethodPut, body); rec.Code != 422 {
		t.Fatalf("foreign member: %d %s", rec.Code, rec.Body.String())
	}
}

func TestNamedKeysAndReferencedPoolDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("api-keys: [synthetic-client]\nrequest-retry: 9\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	call := func(method string, body any) *httptest.ResponseRecorder {
		t.Helper()
		data, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(method, "/access-isolation", bytes.NewReader(data))
		c.Request.Header.Set("Content-Type", "application/json")
		if method == http.MethodGet {
			h.GetAccessIsolation(c)
		} else {
			h.PutAccessIsolation(c)
		}
		return rec
	}
	var view struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(call(http.MethodGet, nil).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	settings := config.AccessIsolation{Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "claude"}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-client", Name: "Alice", Note: "Synthetic team", Pool: "one"}}}
	if rec := call(http.MethodPut, map[string]any{"revision": view.Revision, "settings": settings}); rec.Code != 200 {
		t.Fatalf("save binding: %d %s", rec.Code, rec.Body.String())
	}
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.RequestRetry != 9 || len(reloaded.APIKeys) != 1 || reloaded.AccessIsolation.Keys[0].Name != "Alice" || reloaded.AccessIsolation.Keys[0].Note != "Synthetic team" {
		t.Fatal("binding round trip lost data")
	}
	if err := json.Unmarshal(call(http.MethodGet, nil).Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	settings.Pools = nil
	if rec := call(http.MethodPut, map[string]any{"revision": view.Revision, "settings": settings}); rec.Code != 422 {
		t.Fatalf("deleted referenced pool: %d %s", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/config.yaml", bytes.NewBufferString("api-keys: [synthetic-client]\naccess-isolation:\n  pools: []\n  keys:\n    - key: synthetic-client\n      pool: one\n"))
	h.PutConfigYAML(c)
	if rec.Code != 422 {
		t.Fatalf("YAML deleted referenced pool: %d %s", rec.Code, rec.Body.String())
	}
	settings.Keys[0].Pool = ""
	if rec := call(http.MethodPut, map[string]any{"revision": view.Revision, "settings": settings}); rec.Code != 200 {
		t.Fatalf("unassign and delete: %d %s", rec.Code, rec.Body.String())
	}
}
