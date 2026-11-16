package openai

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

// This is a preservation diagnostic: the local forwarder already consults its
// replay guard after response.created and has no upstream duplex bypass branch.
func TestForwardResponsesCreatedQuotaConsultsReplayGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, frame := range []string{
		`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"exhausted"}}`,
		`{"type":"error","status":429,"error":{"code":"model_cooldown","message":"cooling"}}`,
	} {
		t.Run(gjson.Get(frame, "error.type").String()+gjson.Get(frame, "error.code").String(), func(t *testing.T) {
			result := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
				if err != nil {
					result <- err
					return
				}
				defer func() { _ = conn.Close() }()
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = r
				data := make(chan []byte, 2)
				data <- []byte(`{"type":"response.created","response":{"id":"r-local-guard"}}`)
				data <- []byte(frame)
				close(data)
				errs := make(chan *interfaces.ErrorMessage)
				close(errs)
				calls, canceled := 0, 0
				h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
				_, _, _, got, forwardErr := h.forwardResponsesWebsocket(ctx, newResponsesWebsocketWriter(conn), func(...interface{}) { canceled++ }, data, errs, newInMemoryWebsocketTimelineLog(), "guard-preservation", responsesWebsocketForwardOptions{
					suppressError: func(message *interfaces.ErrorMessage) bool {
						calls++
						return message != nil && message.StatusCode == 429
					},
				})
				if calls != 1 || canceled != 1 || got == nil || got.StatusCode != 429 || forwardErr != nil {
					result <- fmt.Errorf("guard calls=%d canceled=%d error=%v forward=%v", calls, canceled, got, forwardErr)
					return
				}
				result <- nil
			}))
			defer server.Close()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close() }()
			if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			_, first, err := conn.ReadMessage()
			if err != nil || gjson.GetBytes(first, "type").String() != "response.created" {
				t.Fatalf("created frame = %s, error=%v", first, err)
			}
			if _, extra, err := conn.ReadMessage(); err == nil {
				t.Fatalf("quota frame bypassed guard: %s", extra)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
		})
	}
}
