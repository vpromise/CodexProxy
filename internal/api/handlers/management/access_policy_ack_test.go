package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"gopkg.in/yaml.v3"
)

func TestPolicyAcknowledgementMustFenceNewRequests(t *testing.T) {
	for _, endpoint := range []string{"isolation", "yaml"} {
		for _, initiallyEnabled := range []bool{false, true} {
			name := "enable"
			if initiallyEnabled {
				name = "revoke"
			}
			t.Run(endpoint+"/"+name, func(t *testing.T) {
				account := &coreauth.Auth{ID: "synthetic-oauth", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-account"}}
				path := filepath.Join(t.TempDir(), "config.yaml")
				if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				cfg.AccessIsolation = config.AccessIsolation{Enabled: initiallyEnabled,
					Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{coreauth.AccountIdentity(account)}}},
					Keys:  []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}},
				}
				cfg.APIKeys = []string{"synthetic-client"}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				if _, err := manager.Register(context.Background(), account); err != nil {
					t.Fatal(err)
				}
				data, err := yaml.Marshal(cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}
				caller := coreauth.WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
				staleAllowed := false
				started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
				h.SetConfigReloadHook(func(_ context.Context, next *config.Config) {
					close(started)
					<-release
					manager.SetConfig(cfg)
					staleAllowed = coreauth.AccessScopeFromContext(manager.NewAccessRequest(caller)).Allows(account)
					h.SetConfig(cfg)
					staleAllowed = staleAllowed || coreauth.AccessScopeFromContext(manager.NewAccessRequest(caller)).Allows(account)
					manager.SetConfig(next)
					close(finished)
				})
				var releaseOnce sync.Once
				unblock := func() { releaseOnce.Do(func() { close(release) }) }
				t.Cleanup(unblock)
				policy := cfg.CloneForRuntime().AccessIsolation
				policy.Enabled = true
				policy.Pools[0].Accounts = nil
				revision, err := h.accessRevision()
				if err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(map[string]any{"revision": revision, "settings": policy})
				response := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(response)
				c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/access-isolation", bytes.NewReader(body))
				if endpoint == "yaml" {
					next := cfg.CloneForRuntime()
					next.AccessIsolation = policy
					raw, err := yaml.Marshal(next)
					if err != nil {
						t.Fatal(err)
					}
					c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", bytes.NewReader(raw))
					h.PutConfigYAML(c)
				} else {
					h.PutAccessIsolation(c)
				}
				if response.Code != 200 {
					t.Fatalf("policy save status: %d: %s", response.Code, response.Body.String())
				}
				select {
				case <-started:
				case <-time.After(time.Second):
					t.Fatal("reload callback was not dispatched")
				}
				disk, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				if !disk.AccessIsolation.Enabled || len(disk.AccessIsolation.Pools[0].Accounts) != 0 {
					t.Fatal("policy did not persist")
				}
				scope := coreauth.AccessScopeFromContext(manager.NewAccessRequest(caller))
				allowedBeforeApply := scope.Allows(account)
				unblock()
				select {
				case <-finished:
				case <-time.After(time.Second):
					t.Fatal("runtime apply did not complete")
				}
				if staleAllowed {
					t.Fatal("stale runtime temporarily reopened the acknowledged grant")
				}
				if coreauth.AccessScopeFromContext(manager.NewAccessRequest(caller)).Allows(account) {
					t.Fatal("control failed: account remains allowed after runtime apply")
				}
				if allowedBeforeApply {
					t.Fatal("management returned 200 and persisted the restriction, but a NEW request still permits the removed account")
				}
			})
		}
	}
}
