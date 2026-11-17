package executor

import (
	"context"
	"fmt"
	"net/http/httptest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCodexSyncWebsocketErrorPreservesScalarAndSequence(t *testing.T) {
	for _, tc := range []struct{ name, payload, message string }{
		{"scalar", `{"type":"error","status":400,"error":"upstream rejected the request","sequence_number":17,"headers":{"x-request-id":"fixture"}}`, "upstream rejected the request"},
		{"wrapped scalar", `{"type":"error","status":400,"body":{"error":"wrapped failure"},"sequence_number":17,"headers":{"x-request-id":"fixture"}}`, "wrapped failure"},
		{"null outer", `{"type":"error","status":400,"error":null,"response":{"error":{"message":"response failure"}},"sequence_number":17,"headers":{"x-request-id":"fixture"}}`, "response failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err, ok := parseCodexWebsocketError([]byte(tc.payload))
			if !ok || err == nil {
				t.Fatal("error not recognized")
			}
			if got := gjson.Get(err.Error(), "error.message").String(); got != tc.message {
				t.Errorf("message=%q, want %q", got, tc.message)
			}
			if got := gjson.Get(err.Error(), "sequence_number").Int(); got != 17 {
				t.Errorf("sequence=%d", got)
			}
			wire := err.(statusErrWithHeaders)
			if wire.StatusCode() != http.StatusBadRequest || wire.credentialScoped || wire.RetryAfter() != nil {
				t.Errorf("classification changed: %+v", wire)
			}
			if wire.Headers().Get("X-Request-Id") != "fixture" {
				t.Error("request header lost")
			}
			if tc.name == "wrapped scalar" && gjson.Get(err.Error(), "body.error").String() != tc.message {
				t.Error("original body lost")
			}
		})
	}
}

func TestCodexSyncTerminalWrappedErrorPreservesDetails(t *testing.T) {
	for _, payload := range []string{
		`{"type":"error","body":{"error":{"message":"wrapped failure","param":9007199254740993}},"sequence_number":17}`,
		`{"type":"response.failed","error":null,"body":{"error":{"message":"wrapped failure","param":9007199254740993}},"sequence_number":17}`,
	} {
		body, ok := codexTerminalFailureBody([]byte(payload))
		if !ok {
			t.Fatal("failure not recognized")
		}
		if got := gjson.GetBytes(body, "error.message").String(); got != "wrapped failure" {
			t.Errorf("message=%q", got)
		}
		if got := gjson.GetBytes(body, "error.param").Raw; got != "9007199254740993" {
			t.Errorf("param=%s", got)
		}
		if got := gjson.GetBytes(body, "sequence_number").Int(); got != 17 {
			t.Errorf("sequence=%d", got)
		}
	}
}

func TestCodexSyncErrorRecognitionAndQuotaContractsRemain(t *testing.T) {
	for _, payload := range []string{
		`{"status":400,"error":"typeless"}`,
		`{"type":"response.metadata","status":400,"error":"metadata"}`,
		`{"type":"error","error":{"message":"no explicit status"}}`,
	} {
		if _, ok := parseCodexWebsocketError([]byte(payload)); ok {
			t.Errorf("expanded error recognition: %s", payload)
		}
	}
	for _, tc := range []struct {
		payload               string
		status                int
		credential, zeroRetry bool
	}{
		{`{"type":"error","status":401,"error":{"type":"authentication_error","message":"rejected"}}`, 401, false, false},
		{`{"type":"error","status":429,"error":{"type":"usage_limit_reached","message":"quota exhausted"}}`, 429, true, false},
		{`{"type":"error","status":429,"error":{"code":"usage_limit_reached","message":"quota exhausted"}}`, 429, false, false},
		{`{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached"}}`, 429, false, true},
	} {
		err, ok := parseCodexWebsocketError([]byte(tc.payload))
		if !ok || err == nil {
			t.Fatal("known error lost")
		}
		wire := err.(statusErrWithHeaders)
		if wire.StatusCode() != tc.status || wire.credentialScoped != tc.credential {
			t.Errorf("classification changed: %+v", wire)
		}
		if tc.zeroRetry && (wire.RetryAfter() == nil || *wire.RetryAfter() != 0) {
			t.Error("connection-limit retry contract changed")
		}
	}
}

func TestCodexSyncExecutorErrorEnvelopeAcrossTransports(t *testing.T) {
	for _, transport := range []string{"sse", "websocket"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", transport, stream), func(t *testing.T) {
				payload := `{"type":"error","status":400,"body":{"error":{"type":"invalid_request_error","code":"fixture_error","message":"original failure"}},"sequence_number":17}`
				if transport == "websocket" {
					payload = `{"type":"error","status":400,"error":"original failure","sequence_number":17}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if transport == "sse" {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", payload)
						return
					}
					conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Error(errUpgrade)
						return
					}
					defer func() {
						if errClose := conn.Close(); errClose != nil {
							t.Error(errClose)
						}
					}()
					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						t.Error(errRead)
						return
					}
					if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
						t.Error(errWrite)
					}
				}))
				defer server.Close()
				var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(&config.Config{})
				if transport == "websocket" {
					executor = NewCodexWebsocketsExecutor(&config.Config{})
				}
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture"}}
				req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(`{"model":"gpt-6.1-sol","input":"hello"}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: stream}
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var final error
				if !stream {
					_, final = executor.Execute(ctx, auth, req, opts)
				} else {
					result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
					final = errStream
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								final = chunk.Err
							}
						}
					}
				}
				if final == nil {
					t.Fatal("missing terminal error")
				}
				if got := gjson.Get(final.Error(), "error.message").String(); got != "original failure" {
					t.Errorf("message=%q: %v", got, final)
				}
				if got := gjson.Get(final.Error(), "sequence_number").Int(); got != 17 {
					t.Errorf("sequence=%d: %v", got, final)
				}
				if got := final.(interface{ StatusCode() int }).StatusCode(); got != 400 {
					t.Errorf("status=%d", got)
				}
			})
		}
	}
}
