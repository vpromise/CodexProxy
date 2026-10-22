package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClientKeysApplyBeforeAcknowledgementWithoutRuntimeReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeys = []string{"synthetic-old"}
	cfg.AuthDir = t.TempDir()
	if err := config.SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	server := NewServer(cfg, manager, sdkaccess.NewManager(), path,
		WithConfigReloadHook(func(context.Context, *config.Config) {}))
	old := cfg.CloneForRuntime()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", bytes.NewBufferString(`["synthetic-new"]`))
	server.mgmt.PutAPIKeys(c)
	if response.Code != 200 {
		t.Fatalf("save status %d", response.Code)
	}
	assertKeys := func() {
		t.Helper()
		for key, status := range map[string]int{"synthetic-old": 401, "synthetic-new": 200} {
			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+key)
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)
			if rec.Code != status {
				t.Fatalf("client auth status %d, want %d", rec.Code, status)
			}
		}
	}
	assertKeys()
	// Replay a pre-save runtime snapshot through the real server reload path.
	server.UpdateClients(old)
	assertKeys()
	// A later external file edit must still be accepted normally.
	next := old.CloneForRuntime()
	next.APIKeys = []string{"synthetic-external"}
	if err := config.SaveConfigPreserveComments(path, next); err != nil {
		t.Fatal(err)
	}
	server.UpdateClients(next)
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer synthetic-external")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("external config edit rejected: %d", rec.Code)
	}
}
