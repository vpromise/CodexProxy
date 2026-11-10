package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"gopkg.in/yaml.v3"
)

func TestPoolEgressSavePublishesBeforeExecutorReload(t *testing.T) {
	for _, endpoint := range []string{"isolation", "yaml"} {
		t.Run(endpoint, func(t *testing.T) {
			path := writeTestConfigFile(t)
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.APIKeys = []string{"synthetic-client"}
			cfg.AccessIsolation = config.AccessIsolation{Enabled: true,
				Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "127.0.0.1"}}},
				Keys:  []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}},
			}
			if errSave := config.SaveConfigPreserveComments(path, cfg); errSave != nil {
				t.Fatal(errSave)
			}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}
			caller := coreauth.WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
			active := manager.NewAccessRequest(caller)
			release, done := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			reopened := false
			h.SetConfigReloadHook(func(_ context.Context, next *config.Config) {
				<-release
				manager.SetConfig(cfg)
				h.SetConfig(cfg)
				reopened = proxyutil.EgressFromContext(manager.NewAccessRequest(caller)).SourceIP != "127.0.0.2"
				manager.SetConfig(next)
				close(done)
			})
			t.Cleanup(unblock)
			next := cfg.CloneForRuntime()
			next.AccessIsolation.Pools[0].Egress.SourceIP = "127.0.0.2"
			revision, err := h.accessRevision()
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(map[string]any{"revision": revision, "settings": next.AccessIsolation})
			if err != nil {
				t.Fatal(err)
			}
			if endpoint == "yaml" {
				body, err = yaml.Marshal(next)
				if err != nil {
					t.Fatal(err)
				}
			}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
			if endpoint == "yaml" {
				h.PutConfigYAML(c)
			} else {
				h.PutAccessIsolation(c)
			}
			if rec.Code != 200 {
				t.Fatalf("save failed: %d %s", rec.Code, rec.Body.String())
			}
			if got := proxyutil.EgressFromContext(manager.NewAccessRequest(caller)).SourceIP; got != "127.0.0.2" {
				t.Fatalf("acknowledged egress was not active: %s", got)
			}
			if got := proxyutil.EgressFromContext(active).SourceIP; got != "127.0.0.1" {
				t.Fatal("active request changed its route")
			}
			disk, err := config.LoadConfig(path)
			if err != nil || disk.AccessIsolation.Pools[0].Egress.SourceIP != "127.0.0.2" {
				t.Fatalf("egress was not persisted: %v", err)
			}
			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("runtime reload did not finish")
			}
			if reopened {
				t.Fatal("stale runtime restored the old egress")
			}
		})
	}
}
