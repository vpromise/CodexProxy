package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestCodexPoolEgressWebsocketReconnectsWhenRouteChanges(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.ProxyURL = "http://127.0.0.1:1"
	executor := NewCodexWebsocketsExecutor(cfg)
	const id = "synthetic-pool-egress-session"
	defer executor.CloseExecutionSession(id)
	session := executor.getOrCreateSession(id)
	account := &cliproxyauth.Auth{ID: "synthetic-account", Provider: "codex", ProxyURL: "http://127.0.0.1:1"}
	url := "ws" + strings.TrimPrefix(upstream.URL, "http")
	ctxA := proxyutil.WithEgress(context.Background(), proxyutil.Egress{Scope: "a", SourceIP: "127.0.0.1"})
	first, _, _, err := executor.ensureUpstreamConn(ctxA, account, session, account.ID, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := existingWebsocketSessionConn(session, account.ID, url, proxyutil.EgressFromContext(ctxA).Key()); got != first {
		t.Fatal("matching route was not reusable")
	}
	ctxB := proxyutil.WithEgress(context.Background(), proxyutil.Egress{Scope: "b", SourceIP: "127.0.0.1"})
	if got, _ := existingWebsocketSessionConn(session, account.ID, url, proxyutil.EgressFromContext(ctxB).Key()); got != nil {
		t.Fatal("continuation reused another pool's connection")
	}
	second, _, _, err := executor.ensureUpstreamConn(ctxB, account, session, account.ID, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("route change retained the previous upstream socket")
	}
}
