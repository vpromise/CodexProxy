package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestNativePutPreservesPoolIdentity(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := &config.Config{
				CodexKey:  []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "enrolled-record"}},
				ClaudeKey: []config.ClaudeKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "enrolled-record"}},
			}
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			member := coreauth.AccountIdentity(&coreauth.Auth{Provider: provider, Attributes: map[string]string{"api_key": "synthetic-key", "pool_account_id": "enrolled-record"}})
			cfg.AccessIsolation.Pools = []config.AccountPool{{ID: "one", Name: "One", Provider: provider, Accounts: []string{member}}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`[{"api-key":"synthetic-key","base-url":"https://synthetic.invalid","prefix":"edited"}]`))
			if provider == "codex" {
				h.PutCodexKeys(c)
			} else {
				h.PutClaudeKeys(c)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("save status: %d %s", rec.Code, rec.Body.String())
			}
			disk, err := config.LoadConfig(h.configFilePath)
			if err != nil {
				t.Fatal(err)
			}
			id := disk.CodexKey[0].PoolAccountID
			if provider == "claude" {
				id = disk.ClaudeKey[0].PoolAccountID
			}
			if id != "enrolled-record" {
				t.Fatalf("same-key edit lost durable pool identity: got %q", id)
			}
			if !slices.Contains(disk.AccessIsolation.Pools[0].Accounts, member) {
				t.Fatal("same-key edit revoked existing membership")
			}
		})
	}
}

func TestNativeKeySaveFailureKeepsGrantedPolicy(t *testing.T) {
	account := &coreauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "synthetic-key", "pool_account_id": "record-one"}}
	member := coreauth.AccountIdentity(account)
	cfg := &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "record-one"}}}
	cfg.APIKeys = []string{"synthetic-client"}
	cfg.AccessIsolation = config.AccessIsolation{Enabled: true,
		Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{member}}},
		Keys:  []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	h := &Handler{cfg: cfg.CloneForRuntime(), configFilePath: t.TempDir(), authManager: manager}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodDelete, "/?index=0", nil)
	h.DeleteCodexKey(c)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected failed persistence, got %d", rec.Code)
	}
	if len(h.cfg.CodexKey) != 1 || !slices.Contains(h.cfg.AccessIsolation.Pools[0].Accounts, member) {
		t.Fatal("failed save changed handler credentials or membership")
	}
	caller := coreauth.WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
	if !coreauth.AccessScopeFromContext(manager.NewAccessRequest(caller)).Allows(account) {
		t.Fatal("failed save published a revocation")
	}
}

func TestNativePutDoesNotGiveNewDuplicateExistingMembership(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.CodexKey = []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "record-one"}}
			cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "record-one"}}
			member := coreauth.AccountIdentity(&coreauth.Auth{Provider: provider, Attributes: map[string]string{"api_key": "synthetic-key", "pool_account_id": "record-one"}})
			cfg.AccessIsolation.Pools = []config.AccountPool{{ID: "one", Name: "One", Provider: provider, Accounts: []string{member}}}
			h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(`[{"api-key":"synthetic-key","base-url":"https://synthetic.invalid","pool-account-id":"record-one"},{"api-key":"synthetic-key","base-url":"https://synthetic.invalid","prefix":"new"}]`))
			if provider == "codex" {
				h.PutCodexKeys(c)
			} else {
				h.PutClaudeKeys(c)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("save status: %d %s", rec.Code, rec.Body.String())
			}
			accounts, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: h.cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
			if err != nil {
				t.Fatal(err)
			}
			for _, account := range accounts {
				if account.Provider == provider && account.Prefix == "new" && coreauth.AccountIdentity(account) == member {
					t.Fatal("new duplicate inherited the existing record's pool membership")
				}
			}
			if !slices.Contains(h.cfg.AccessIsolation.Pools[0].Accounts, member) {
				t.Fatal("adding a duplicate revoked the existing record")
			}
		})
	}
}

func TestPoolIdentityDoesNotMapPluginToMatchingNativeKey(t *testing.T) {
	h := &Handler{cfg: &config.Config{CodexKey: []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "native-record"}}}}
	plugin := &coreauth.Auth{ID: "plugin-account", Provider: "codex", Attributes: map[string]string{"api_key": "synthetic-key", "plugin_id": "synthetic-plugin"}}
	if id := h.persistedPoolIdentity(plugin); id != "" {
		t.Fatal("plugin account inherited a native record identity")
	}
}

func TestPoolInventoryKeepsDistinctNativeRecords(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, enrolled := range []bool{false, true} {
			name := provider + "/legacy-enrollment"
			if enrolled {
				name = provider + "/enrolled"
			}
			t.Run(name, func(t *testing.T) {
				cfg := &config.Config{}
				cfg.CodexKey = []config.CodexKey{
					{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", Prefix: "first"},
					{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", Prefix: "second"},
				}
				cfg.ClaudeKey = []config.ClaudeKey{
					{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", Prefix: "first"},
					{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", Prefix: "second"},
				}
				if provider == "codex" {
					cfg.ClaudeKey = nil
				} else {
					cfg.CodexKey = nil
				}
				if enrolled {
					for i := range cfg.CodexKey {
						cfg.CodexKey[i].PoolAccountID = cfg.CodexKey[i].Prefix
					}
					for i := range cfg.ClaudeKey {
						cfg.ClaudeKey[i].PoolAccountID = cfg.ClaudeKey[i].Prefix
					}
				}
				accounts, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil {
					t.Fatal(err)
				}
				manager := coreauth.NewManager(nil, nil, nil)
				for _, account := range accounts {
					if _, errRegister := manager.Register(context.Background(), account); errRegister != nil {
						t.Fatal(errRegister)
					}
				}
				// Enrollment updates configuration before the asynchronous runtime reload.
				for i := range cfg.CodexKey {
					cfg.CodexKey[i].PoolAccountID = cfg.CodexKey[i].Prefix
				}
				for i := range cfg.ClaudeKey {
					cfg.ClaudeKey[i].PoolAccountID = cfg.ClaudeKey[i].Prefix
				}
				h := &Handler{cfg: cfg, authManager: manager}
				assertInventory := func() {
					t.Helper()
					inventory := h.poolAccounts()
					if len(inventory) != 2 {
						t.Fatalf("two distinct native records collapsed to %d entries", len(inventory))
					}
					for _, entry := range inventory {
						for _, account := range accounts {
							if account.Prefix == entry.Prefix {
								expected := account.Clone()
								expected.Attributes["pool_account_id"] = account.Prefix
								if entry.ID != coreauth.AccountIdentity(expected) {
									t.Fatalf("pool identity does not match displayed record %q", entry.Prefix)
								}
							}
						}
					}
				}
				assertInventory()
				if enrolled {
					// Existing IDs must take precedence over the old config index after reordering.
					if provider == "codex" {
						cfg.CodexKey[0], cfg.CodexKey[1] = cfg.CodexKey[1], cfg.CodexKey[0]
					} else {
						cfg.ClaudeKey[0], cfg.ClaudeKey[1] = cfg.ClaudeKey[1], cfg.ClaudeKey[0]
					}
					assertInventory()
					if provider == "codex" {
						cfg.CodexKey = cfg.CodexKey[:1]
					} else {
						cfg.ClaudeKey = cfg.ClaudeKey[:1]
					}
					if inventory := h.poolAccounts(); len(inventory) != 1 || inventory[0].Prefix != "second" {
						t.Fatalf("removed duplicate record was remapped: %+v", inventory)
					}
				}
			})
		}
	}
}
