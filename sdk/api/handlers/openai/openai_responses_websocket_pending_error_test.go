package openai

import (
	"errors"
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

func TestForwardResponsesWebsocketPreservesPendingErrorOnClose(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name        string
		status      int
		body        string
		replay      bool
		wantClose   int
		wantReason  string
		wantPayload bool
	}{
		{name: "unauthorized replay", status: 401, body: `{"error":{"message":"credential failed"}}`, replay: true, wantClose: 1012, wantReason: wsHTTPReplayRequiredCloseReason},
		{name: "rate limit replay", status: 429, body: `{"error":{"message":"credential failed"}}`, replay: true, wantClose: 1012, wantReason: wsHTTPReplayRequiredCloseReason},
		{name: "message too big", status: 413, body: `{"error":{"message":"message too big","code":"message_too_big"}}`, wantClose: 1009, wantReason: "message too big"},
		{name: "request fault", status: 400, body: `{"error":{"message":"invalid request","type":"invalid_request_error"}}`, wantClose: 1006, wantReason: "unexpected EOF", wantPayload: true},
		{name: "upstream failure stays silent", status: 502, body: `{"error":{"message":"upstream failed"}}`, wantClose: 1006, wantReason: "unexpected EOF"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				defer func() { _ = conn.Close() }()
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = r
				want := &interfaces.ErrorMessage{
					StatusCode: tc.status,
					Error: websocketPinnedFailoverStatusError{
						status: tc.status,
						msg:    tc.body,
					},
				}
				data := make(chan []byte)
				errs := make(chan *interfaces.ErrorMessage, 1)
				// The producer publishes its error before closing both channels. Both
				// receives can be ready when forwarding starts or resumes.
				errs <- want
				close(errs)
				close(data)
				writer := newResponsesWebsocketWriter(conn)
				var canceled error
				cancelCalls := 0
				h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
				_, _, _, got, errForward := h.forwardResponsesWebsocket(ctx, writer, func(values ...interface{}) {
					cancelCalls++
					if len(values) > 0 {
						canceled, _ = values[0].(error)
					}
				}, data, errs, newInMemoryWebsocketTimelineLog(), "pending-error", responsesWebsocketForwardOptions{
					suppressError: shouldReplayResponsesWebsocketPinnedAuthFailure,
				})
				if got != want || canceled != want.Error || cancelCalls != 1 {
					serverErr <- fmt.Errorf("forward got error=%#v, cancel=%v (%d calls); want original status %d", got, canceled, cancelCalls, tc.status)
					return
				}
				if tc.replay {
					if errForward != nil || writer.closing.Load() {
						serverErr <- fmt.Errorf("replay forwarding closed the socket: %v", errForward)
						return
					}
					matched, errClose := writer.closeForUpstreamError(responsesWebsocketHTTPReplayRequiredError())
					if !matched || errClose != nil {
						serverErr <- fmt.Errorf("replay close matched=%t, error=%v", matched, errClose)
						return
					}
				} else if !errors.Is(errForward, websocket.ErrCloseSent) {
					serverErr <- fmt.Errorf("forward error = %v, want ErrCloseSent", errForward)
					return
				}
				serverErr <- nil
			}))
			defer server.Close()

			// Repetition exercises both ready select cases without sleeps or
			// production scheduling hooks.
			for i := 0; i < 32; i++ {
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
				if err != nil {
					t.Fatalf("dial websocket: %v", err)
				}
				if errDeadline := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
					_ = conn.Close()
					t.Fatalf("set test read deadline: %v", errDeadline)
				}
				_, payload, errRead := conn.ReadMessage()
				if tc.wantPayload && errRead == nil {
					if gjson.GetBytes(payload, "type").String() != "error" || gjson.GetBytes(payload, "status").Int() != int64(tc.status) {
						_ = conn.Close()
						t.Fatalf("iteration %d: payload=%s, want status %d error", i, payload, tc.status)
					}
					_, _, errRead = conn.ReadMessage()
				} else if tc.wantPayload {
					_ = conn.Close()
					t.Fatalf("iteration %d: expected request error payload, got %v; server=%v", i, errRead, <-serverErr)
				}
				_ = conn.Close()
				errServer := <-serverErr
				var closeErr *websocket.CloseError
				if !errors.As(errRead, &closeErr) || closeErr.Code != tc.wantClose || closeErr.Text != tc.wantReason || errServer != nil {
					t.Fatalf("iteration %d: client=%v, server=%v; want close %d %q", i, errRead, errServer, tc.wantClose, tc.wantReason)
				}
			}
		})
	}
}
