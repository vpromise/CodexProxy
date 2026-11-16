package executor

import (
	"context"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConsoleWebsocketUsesBoundSourceAndRejectsStaleOwner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") {
			t.Errorf("source %s", r.RemoteAddr)
		}
		conn, e := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if e == nil {
			conn.Close()
		}
	}))
	defer server.Close()
	b := &cliproxyauth.ConsoleBinding{NodeID: "n", InstanceID: "i", ProfileID: "p", ProfileVersion: "1", ProxyURL: "direct-ip://127.0.0.1"}
	cfg := &config.Config{ConsoleNode: config.ConsoleNodeConfig{Enabled: true, NodeID: b.NodeID, InstanceID: b.InstanceID, EgressProfiles: []config.ConsoleEgressProfile{{ID: b.ProfileID, Version: b.ProfileVersion, ProxyURL: b.ProxyURL}}}}
	a := &cliproxyauth.Auth{ProxyURL: b.ProxyURL, Metadata: map[string]any{"console_binding": b}}
	dialer := newProxyAwareWebsocketDialer(cfg, a, context.Background())
	if dialer.Proxy != nil {
		t.Fatal("environment proxy retained")
	}
	conn, _, e := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if e != nil {
		t.Fatal(e)
	}
	conn.Close()
	cfg.ConsoleNode.InstanceID = "other"
	dialer = newProxyAwareWebsocketDialer(cfg, a)
	if _, _, e = dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil); e == nil {
		t.Fatal("stale owner sent websocket")
	}
}
