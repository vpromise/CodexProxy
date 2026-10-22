package live

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRealtimeExistingSocketRejectsNewOperationsAfterPoolRemoval(t *testing.T) {
	var messages atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := sidebandUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			kind, body, err := conn.ReadMessage()
			if err != nil {
				return
			}
			messages.Add(1)
			_ = conn.WriteMessage(kind, body)
		}
	}))
	t.Cleanup(upstream.Close)
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{})
	account := &auth.Auth{ID: "synthetic-oauth", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "synthetic-token", "account_id": "synthetic-subject"}}
	registerCredential(t, manager, account)
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{auth.AccountIdentity(account)}}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}}}}
	cfg.APIKeys = []string{"synthetic-client"}
	manager.SetConfig(cfg)
	handler := NewHandler(manager, cfg)
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1"
	router := gin.New()
	router.GET("/v1/realtime", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithClientIdentity(c.Request.Context(), "synthetic-client", "config-inline"))
		c.Next()
	}, handler.HandleRealtimeWebsocket)
	downstream := httptest.NewServer(router)
	t.Cleanup(downstream.Close)
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	if _, body, err := conn.ReadMessage(); err != nil || !strings.Contains(string(body), "response.create") {
		t.Fatalf("authorized message: %s %v", body, err)
	}
	cfg = cfg.CloneForRuntime()
	cfg.AccessIsolation.Pools[0].Accounts = nil
	manager.SetConfig(cfg)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); err != nil {
		t.Fatal(err)
	}
	if _, body, err := conn.ReadMessage(); err != nil || !strings.Contains(string(body), "access_denied") {
		t.Fatalf("removed membership: %s %v", body, err)
	}
	if messages.Load() != 1 {
		t.Fatalf("unauthorized messages reached upstream: %d", messages.Load())
	}
}

func TestRealtimeSecretCreationRequiresAuthorizedCodexOAuth(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{})
	account := &auth.Auth{ID: "synthetic-claude", Provider: "claude", Status: auth.StatusActive, Metadata: map[string]any{"account_uuid": "synthetic-account", "organization_uuid": "synthetic-org", "access_token": "synthetic-token"}}
	registerCredential(t, manager, account)
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "claude", Accounts: []string{auth.AccountIdentity(account)}}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}}}}
	cfg.APIKeys = []string{"synthetic-client"}
	manager.SetConfig(cfg)
	handler := NewHandler(manager, cfg)
	router := gin.New()
	router.POST("/v1/realtime/client_secrets", func(c *gin.Context) {
		c.Request = c.Request.WithContext(auth.WithClientIdentity(c.Request.Context(), "synthetic-client", "config-inline"))
		c.Set("userApiKey", "synthetic-client")
		c.Set("accessProvider", "config-inline")
		c.Next()
	}, handler.CreateClientSecret)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("Claude key minted Codex secret: %d %s", rec.Code, rec.Body.String())
	}
}
