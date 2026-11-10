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
	"github.com/pion/webrtc/v4"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPoolEgressWebRTCCandidatesUseOnlyBoundIPv4(t *testing.T) {
	api, err := newPionAPIWithOptions(config.CodexLiveMediaRelayConfig{}, false, false, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if errClose := connection.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	if _, errChannel := connection.CreateDataChannel("synthetic", nil); errChannel != nil {
		t.Fatal(errChannel)
	}
	offer, err := connection.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(connection)
	if errSet := connection.SetLocalDescription(offer); errSet != nil {
		t.Fatal(errSet)
	}
	select {
	case <-gathered:
	case <-time.After(5 * time.Second):
		t.Fatal("ICE gathering did not finish")
	}
	candidates := 0
	for _, line := range strings.Split(connection.LocalDescription().SDP, "\n") {
		if strings.HasPrefix(line, "a=candidate:") {
			fields := strings.Fields(line)
			if len(fields) < 5 || fields[4] != "127.0.0.1" {
				t.Fatalf("unexpected local candidate: %s", line)
			}
			candidates++
		}
	}
	if candidates == 0 {
		t.Fatal("no bound local candidate was gathered")
	}
}

func TestPoolEgressRealtimeSocketRequiresReconnectAfterChange(t *testing.T) {
	var forwarded atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := sidebandUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			kind, body, errRead := conn.ReadMessage()
			if errRead != nil {
				return
			}
			forwarded.Add(1)
			if errWrite := conn.WriteMessage(kind, body); errWrite != nil {
				return
			}
		}
	}))
	defer upstream.Close()
	manager := auth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&captureExecutor{})
	account := &auth.Auth{ID: "synthetic-egress", Provider: "codex", Status: auth.StatusActive, Metadata: map[string]any{"access_token": "synthetic-token", "account_id": "synthetic-principal"}}
	registerCredential(t, manager, account)
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true,
		Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{auth.AccountIdentity(account)}, Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "127.0.0.1"}}},
		Keys:  []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}},
	}}
	cfg.APIKeys = []string{"synthetic-client"}
	manager.SetConfig(cfg)
	handler := NewHandler(manager, cfg)
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1"
	router := gin.New()
	router.GET("/v1/realtime", func(c *gin.Context) {
		caller := auth.WithClientIdentity(c.Request.Context(), "synthetic-client", "config-inline")
		c.Request = c.Request.WithContext(manager.NewAccessRequest(caller))
		c.Next()
	}, handler.HandleRealtimeWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/realtime", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if errDeadline := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
		t.Fatal(errDeadline)
	}
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatal(errRead)
	}
	next := cfg.CloneForRuntime()
	next.AccessIsolation.Pools[0].Egress.SourceIP = "127.0.0.2"
	manager.SetConfig(next)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, body, errRead := conn.ReadMessage()
	if errRead != nil || !strings.Contains(string(body), "egress_changed") {
		t.Fatalf("missing reconnect error: %s %v", body, errRead)
	}
	if forwarded.Load() != 1 {
		t.Fatal("new operation reached the old upstream exit")
	}
}

func TestPoolEgressWebRTCRejectsUnavailableAddress(t *testing.T) {
	if _, err := newPionAPIWithOptions(config.CodexLiveMediaRelayConfig{}, false, false, "203.0.113.250"); err == nil {
		t.Fatal("unavailable address was accepted")
	}
}
