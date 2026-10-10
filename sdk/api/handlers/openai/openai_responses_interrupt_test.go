package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

// TestResponsesInterruptInFlight verifies that a Codex response.interrupt sent while
// generation is running is forwarded unchanged to the current upstream socket, and that
// the same socket accepts the next response.create. Unknown idle, malformed, and disabled-auth
// interrupts must fail locally without opening or writing that socket.
func TestResponsesInterruptInFlight(t *testing.T) {
	for _, frameType := range []int{websocket.TextMessage, websocket.BinaryMessage} {
		t.Run(fmt.Sprintf("frame_%d", frameType), func(t *testing.T) {
			interrupt := []byte(`{"type":"response.interrupt","response_id":"r1","mode":"discard_partial_items","extension":"keep"}`)
			var connections atomic.Int32
			upstreamDone := make(chan struct{})
			var upstreamDoneOnce sync.Once
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				connections.Add(1)
				defer upstreamDoneOnce.Do(func() { close(upstreamDone) })
				c, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade upstream: %v", errUpgrade)
					return
				}
				defer func() { _ = c.Close() }()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				read := func() []byte {
					_, payload, errRead := c.ReadMessage()
					if errRead != nil {
						t.Errorf("upstream read: %v", errRead)
					}
					return payload
				}
				write := func(payload string) {
					if errWrite := c.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Errorf("upstream write: %v", errWrite)
					}
				}
				if payload := read(); gjson.GetBytes(payload, "type").String() != "response.create" {
					t.Errorf("expected response.create, got %s", payload)
					return
				}
				write(`{"type":"response.created","response":{"id":"r1"}}`)
				// Hold completion until the in-flight interrupt arrives unchanged.
				if payload := read(); !bytes.Equal(payload, interrupt) {
					t.Errorf("interrupt changed: %s", payload)
					return
				}
				write(`{"type":"response.incomplete","response":{"id":"r1","status":"incomplete","incomplete_details":{"reason":"interrupted"},"usage":{"output_tokens":7},"output":[]}}`)
				if payload := read(); gjson.GetBytes(payload, "type").String() != "response.create" {
					t.Errorf("expected follow-up response.create, got %s", payload)
					return
				}
				write(`{"type":"response.created","response":{"id":"r2"}}`)
				write(`{"type":"response.completed","response":{"id":"r2","status":"completed","output":[]}}`)
				_, _, _ = c.ReadMessage()
			}))
			defer upstream.Close()

			cfg := &config.Config{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetConfig(cfg)
			manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
			authID := fmt.Sprintf("interrupt-%d", frameType)
			model := "interrupt-model-" + authID
			_, errRegister := manager.Register(context.Background(), &coreauth.Auth{
				ID:       authID,
				Provider: "codex",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"api_key":    "test-key",
					"base_url":   upstream.URL,
					"websockets": "true",
				},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(authID)

			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.GET("/v1/responses", handler.ResponsesWebsocket)
			downstream := httptest.NewServer(router)
			defer downstream.Close()

			client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() { _ = client.Close() }()
			_ = client.SetReadDeadline(time.Now().Add(8 * time.Second))
			send := func(payload []byte) {
				t.Helper()
				if errSend := client.WriteMessage(frameType, payload); errSend != nil {
					t.Fatal(errSend)
				}
			}
			expect := func(event string) []byte {
				t.Helper()
				_, payload, errRead := client.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				if got := gjson.GetBytes(payload, "type").String(); got != event {
					t.Fatalf("expected %s, got %s", event, payload)
				}
				return payload
			}

			send([]byte(`{"type":"response.interrupt"}`))
			expect("error")
			send(interrupt)
			expect("error")
			if connections.Load() != 0 {
				t.Fatal("idle interrupt opened an upstream connection")
			}

			send([]byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model)))
			expect("response.created")
			send([]byte(`{"type":"response.interrupt","response_id":42}`))
			expect("error")
			auth, _ := manager.GetByID(authID)
			auth.Disabled = true
			if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			send(interrupt)
			expect("error")
			auth.Disabled = false
			if _, errUpdate := manager.Update(context.Background(), auth); errUpdate != nil {
				t.Fatal(errUpdate)
			}
			send(interrupt)
			ack := expect("response.incomplete")
			if gjson.GetBytes(ack, "response.usage.output_tokens").Int() != 7 || gjson.GetBytes(ack, "response.incomplete_details.reason").String() != "interrupted" {
				t.Fatalf("interrupt acknowledgement lost usage or reason: %s", ack)
			}
			// A completed interrupt must be silent and must not reach the next turn.
			send(interrupt)
			send(interrupt)
			send([]byte(fmt.Sprintf(`{"type":"response.create","model":%q,"previous_response_id":"r1","input":[]}`, model)))
			expect("response.created")
			expect("response.completed")
			send(interrupt)
			send([]byte(`{"type":"response.interrupt","response_id":"unknown"}`))
			expect("error")
			_ = client.Close()
			select {
			case <-upstreamDone:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream did not close")
			}
			if got := connections.Load(); got != 1 {
				t.Fatalf("upstream connections=%d, want 1", got)
			}
		})
	}
}

// HTTP cancellation needs a separate transcript policy. An interrupt without a
// live native socket must leave the current HTTP request and connection usable.
func TestResponsesInterruptDoesNotCancelHTTPFallback(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	finish := func() { releaseOnce.Do(func() { close(release) }) }
	defer finish()
	canceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"http-response\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"http-response\",\"status\":\"completed\",\"output\":[]}}\n\n")
		case <-r.Context().Done():
			close(canceled)
		}
	}))
	defer upstream.Close()
	defer finish()
	cfg := &config.Config{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	const authID = "interrupt-http-fallback"
	const model = "interrupt-http-fallback-model"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: authID, Provider: "codex", Status: coreauth.StatusActive,
		Attributes: map[string]string{"api_key": "fixture-key", "base_url": upstream.URL},
	}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	defer registry.GetGlobalRegistry().UnregisterClient(authID)
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	client, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if errDial != nil {
		t.Fatal(errDial)
	}
	defer func() { _ = client.Close() }()
	_ = client.SetReadDeadline(time.Now().Add(5 * time.Second))
	expect := func(event string) {
		t.Helper()
		_, payload, errRead := client.ReadMessage()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != event {
			t.Fatalf("event=%s, want %s: %s", got, event, payload)
		}
	}
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(`{"type":"response.create","model":%q,"input":[]}`, model))); errWrite != nil {
		t.Fatal(errWrite)
	}
	expect("response.created")
	if errWrite := client.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.interrupt","response_id":"http-response","mode":"discard_partial_items"}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	expect("error")
	select {
	case <-canceled:
		t.Fatal("unsupported HTTP interrupt canceled the active request")
	default:
	}
	finish()
	expect("response.completed")
}
