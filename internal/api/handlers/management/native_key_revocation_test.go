package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"gopkg.in/yaml.v3"
)

func TestNativeCredentialEditsFenceRemovedPoolMembers(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, edit := range []string{"delete-index", "delete-key", "delete-key-base", "put-empty", "put-replace", "patch-replace", "patch-remove", "yaml", "yaml-empty-base", "yaml-whitespace-base"} {
			if provider == "claude" && (edit == "patch-remove" || edit == "yaml-empty-base" || edit == "yaml-whitespace-base") {
				continue
			}
			t.Run(provider+"/"+edit, func(t *testing.T) {
				path := writeTestConfigFile(t)
				cfg, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				if provider == "codex" {
					cfg.CodexKey = []config.CodexKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "record-one"}}
				} else {
					cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "synthetic-key", BaseURL: "https://synthetic.invalid", PoolAccountID: "record-one"}}
				}
				cfg.APIKeys = []string{"synthetic-client"}
				accounts, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: cfg, IDGenerator: synthesizer.NewStableIDGenerator()})
				if err != nil {
					t.Fatal(err)
				}
				account := accounts[0]
				id := coreauth.AccountIdentity(account)
				missing := provider + ":temporarily-missing"
				cfg.AccessIsolation = config.AccessIsolation{Enabled: true,
					Pools: []config.AccountPool{
						{ID: "one", Name: "One", Provider: provider, Accounts: []string{id, missing}},
						{ID: "overlap", Name: "Overlap", Provider: provider, Accounts: []string{id}},
					},
					Keys: []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}},
				}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				if provider == "codex" {
					manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
				} else {
					manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(cfg))
				}
				if _, errRegister := manager.Register(context.Background(), account); errRegister != nil {
					t.Fatal(errRegister)
				}
				if errSave := config.SaveConfigPreserveComments(path, cfg); errSave != nil {
					t.Fatal(errSave)
				}
				h := &Handler{cfg: cfg.CloneForRuntime(), configFilePath: path, authManager: manager}
				previous := cfg.CloneForRuntime()
				caller := coreauth.WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
				active := manager.NewAccessRequest(caller)
				opts := coreexecutor.Options{Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: account.ID}}
				if _, errSelect := manager.SelectAuth(caller, provider, "", opts); errSelect != nil {
					t.Fatalf("control selection failed: %v", errSelect)
				}
				blocked, done := make(chan struct{}), make(chan struct{})
				var unblockOnce sync.Once
				unblock := func() { unblockOnce.Do(func() { close(blocked) }) }
				reopened := false
				h.SetConfigReloadHook(func(_ context.Context, next *config.Config) {
					<-blocked
					// A stale runtime replay must not restore the removed membership.
					manager.SetConfig(previous)
					h.SetConfig(previous)
					reopened = coreauth.AccessScopeFromContext(manager.NewAccessRequest(caller)).Allows(account)
					manager.SetConfig(next)
					close(done)
				})
				t.Cleanup(func() {
					unblock()
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("runtime reload did not finish")
					}
				})
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				method, query, body := http.MethodDelete, "index=0", ""
				handler := h.DeleteCodexKey
				if provider == "claude" {
					handler = h.DeleteClaudeKey
				}
				switch edit {
				case "delete-key":
					query = "api-key=synthetic-key"
				case "delete-key-base":
					query = "api-key=synthetic-key&base-url=https%3A%2F%2Fsynthetic.invalid"
				case "put-empty", "put-replace":
					method, body, handler = http.MethodPut, `[]`, h.PutCodexKeys
					if provider == "claude" {
						handler = h.PutClaudeKeys
					}
					if edit == "put-replace" {
						body = `[{"api-key":"synthetic-replacement","base-url":"https://synthetic.invalid"}]`
					}
				case "patch-replace", "patch-remove":
					method, body, handler = http.MethodPatch, `{"index":0,"value":{"api-key":"synthetic-replacement"}}`, h.PatchCodexKey
					if provider == "claude" {
						handler = h.PatchClaudeKey
					}
					if edit == "patch-remove" {
						body = `{"index":0,"value":{"base-url":""}}`
					}
				case "yaml", "yaml-empty-base", "yaml-whitespace-base":
					next := cfg.CloneForRuntime()
					if edit == "yaml" {
						next.CodexKey, next.ClaudeKey = nil, nil
					} else if edit == "yaml-empty-base" {
						next.CodexKey[0].BaseURL = ""
					} else {
						next.CodexKey[0].BaseURL = "   "
					}
					raw, errMarshal := yaml.Marshal(next)
					if errMarshal != nil {
						t.Fatal(errMarshal)
					}
					method, body, handler = http.MethodPut, string(raw), h.PutConfigYAML
				}
				c.Request = httptest.NewRequest(method, "/?"+query, strings.NewReader(body))
				handler(c)
				if rec.Code != http.StatusOK {
					t.Fatalf("save status: %d %s", rec.Code, rec.Body.String())
				}
				if selected, errSelect := manager.SelectAuth(caller, provider, "", opts); errSelect == nil || selected != nil {
					t.Fatal("acknowledged edit permits a NEW request to select the removed credential")
				}
				if !coreauth.AccessScopeFromContext(active).Allows(account) {
					t.Fatal("removed membership changed an in-progress grant")
				}
				disk, errLoad := config.LoadConfig(path)
				if errLoad != nil {
					t.Fatal(errLoad)
				}
				for _, pool := range disk.AccessIsolation.Pools {
					if slices.Contains(pool.Accounts, id) {
						t.Fatal("revocation was not persisted in every overlapping pool")
					}
				}
				if !slices.Contains(disk.AccessIsolation.Pools[0].Accounts, missing) {
					t.Fatal("unrelated temporarily missing member was removed")
				}
				// The blocked runtime still contains the old credential, proving this
				// rejection comes from the newly published grant, not executor removal.
				if _, ok := manager.GetByID(account.ID); !ok {
					t.Fatal("control failed: runtime credential was already removed")
				}
				unblock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("runtime reload did not finish")
				}
				if reopened || coreauth.AccessScopeFromContext(manager.NewAccessRequest(caller)).Allows(account) {
					t.Fatal("runtime replay restored revoked membership")
				}
			})
		}
	}
}
