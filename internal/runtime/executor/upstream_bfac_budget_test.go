package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func codexMinimumBudgetUpstream(t *testing.T) (*httptest.Server, <-chan []byte) {
	t.Helper()
	requests := make(chan []byte, 4)
	completed := `{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.6-sol","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
	accepts := func(body []byte) bool {
		value := gjson.GetBytes(body, "max_output_tokens")
		return !value.Exists() || value.Type != gjson.Number || value.Int() >= 16
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			conn, errUpgrade := upgrader.Upgrade(w, r, nil)
			if errUpgrade != nil {
				t.Errorf("upgrade: %v", errUpgrade)
				return
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil {
					t.Errorf("close websocket: %v", errClose)
				}
			}()
			_, body, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Errorf("read websocket request: %v", errRead)
				return
			}
			requests <- body
			if !accepts(body) {
				_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"Invalid 'max_output_tokens': integer below minimum value. Expected a value >= 16."}}`))
				return
			}
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(completed)); errWrite != nil {
				t.Errorf("write websocket response: %v", errWrite)
			}
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read HTTP request: %v", errRead)
			return
		}
		requests <- body
		if !accepts(body) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"message":"Invalid 'max_output_tokens': integer below minimum value. Expected a value >= 16.","type":"invalid_request_error","param":"max_output_tokens"}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+completed+"\n\n")
	}))
	t.Cleanup(server.Close)
	return server, requests
}

func TestCodexSmallOutputBudgetsAllSourceFormats_Issue7304(t *testing.T) {
	modelRule := []config.PayloadModelRule{{Name: "gpt-5.6-sol", Protocol: "codex"}}
	matchedRule := []config.PayloadModelRule{{
		Name: "gpt-5.6-sol", Protocol: "codex", Match: []map[string]any{{"max_output_tokens": 16}},
	}}

	cases := []struct {
		name    string
		from    sdktranslator.Format
		payload string
		want    string
		rules   config.PayloadConfig
	}{
		{name: "responses max_output_tokens 1", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":1,"input":"Hi"}`, want: "16"},
		{name: "responses max_output_tokens 15", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":15,"input":"Hi"}`, want: "16"},
		{name: "responses legacy max_completion_tokens 1", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_completion_tokens":1,"input":"Hi"}`, want: "16"},
		{name: "responses legacy max_tokens 1", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_tokens":1,"input":"Hi"}`, want: "16"},
		{name: "responses 16 unchanged", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":16,"input":"Hi"}`, want: "16"},
		{name: "responses 1024 unchanged", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":1024,"input":"Hi"}`, want: "1024"},
		{name: "responses zero untouched", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":0,"input":"Hi"}`, want: "0"},
		{name: "responses negative untouched", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":-5,"input":"Hi"}`, want: "-5"},
		{name: "responses fractional untouched", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":1.5,"input":"Hi"}`, want: "1.5"},
		{name: "responses integral float normalized", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":15.0,"input":"Hi"}`, want: "16"},
		{name: "responses scientific integral normalized", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":1e1,"input":"Hi"}`, want: "16"},
		{name: "responses string untouched", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","max_output_tokens":"1","input":"Hi"}`, want: `"1"`},
		{name: "responses absent", from: sdktranslator.FormatOpenAIResponse, payload: `{"model":"gpt-5.6-sol","input":"Hi"}`},
		{name: "chat max_tokens 1", from: sdktranslator.FormatOpenAI, payload: `{"model":"gpt-5.6-sol","max_tokens":1,"messages":[{"role":"user","content":"Hi"}]}`, want: "16"},
		{name: "chat max_completion_tokens 1", from: sdktranslator.FormatOpenAI, payload: `{"model":"gpt-5.6-sol","max_completion_tokens":1,"messages":[{"role":"user","content":"Hi"}]}`, want: "16"},
		{name: "chat max_output_tokens 1", from: sdktranslator.FormatOpenAI, payload: `{"model":"gpt-5.6-sol","max_output_tokens":1,"messages":[{"role":"user","content":"Hi"}]}`, want: "16"},
		{name: "claude max_tokens 1", from: sdktranslator.FormatClaude, payload: `{"model":"gpt-5.6-sol","max_tokens":1,"messages":[{"role":"user","content":[{"type":"text","text":"Hi"}]}]}`, want: "16"},
		{
			name: "responses payload override still final", from: sdktranslator.FormatOpenAIResponse,
			payload: `{"model":"gpt-5.6-sol","max_output_tokens":1,"input":"Hi"}`, want: "8",
			rules: config.PayloadConfig{Override: []config.PayloadRule{{Models: matchedRule, Params: map[string]any{"max_output_tokens": 8}}}},
		},
		{
			name: "responses payload filter still final", from: sdktranslator.FormatOpenAIResponse,
			payload: `{"model":"gpt-5.6-sol","max_output_tokens":1,"input":"Hi"}`,
			rules:   config.PayloadConfig{Filter: []config.PayloadFilterRule{{Models: modelRule, Params: []string{"max_output_tokens"}}}},
		},
	}

	for _, tc := range cases {
		for _, useWebsocket := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/websocket=%t/stream=%t", tc.name, useWebsocket, stream), func(t *testing.T) {
					server, requests := codexMinimumBudgetUpstream(t)
					cfg := &config.Config{Payload: tc.rules}
					var executor cliproxyauth.ProviderExecutor = NewCodexExecutor(cfg)
					if useWebsocket {
						executor = NewCodexWebsocketsExecutor(cfg)
					}
					req := cliproxyexecutor.Request{Model: "gpt-5.6-sol", Payload: []byte(tc.payload)}
					opts := cliproxyexecutor.Options{SourceFormat: tc.from, Stream: stream}
					auth := codexOAuthTestAuth(server.URL)
					auth.ID = "codex-output-budget-7304-test"

					// wantAccept mirrors the simplified upstream model above: any
					// numeric budget below 16 is rejected (the simulator does not
					// distinguish integer from fractional), so accepts cover normalized,
					// valid, absent, and non-numeric values. Accepted requests must
					// succeed; rejected ones must surface a budget error.
					wantAccept := true
					if tc.want != "" {
						if parsed, errParse := strconv.ParseFloat(tc.want, 64); errParse == nil && parsed < 16 {
							wantAccept = false
						}
					}
					var execErr error
					if stream {
						result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
						execErr = errStream
						if errStream == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil && execErr == nil {
									execErr = chunk.Err
								}
							}
						}
					} else {
						_, execErr = executor.Execute(context.Background(), auth, req, opts)
					}
					if wantAccept && execErr != nil {
						t.Fatalf("execution must succeed for an upstream-acceptable budget, got error: %v", execErr)
					}
					if !wantAccept && execErr == nil {
						t.Fatal("execution must fail when the untouched budget stays below the upstream minimum")
					}
					if !wantAccept && execErr != nil && !strings.Contains(execErr.Error(), "max_output_tokens") {
						t.Fatalf("rejected execution error must propagate the upstream budget error, got: %v", execErr)
					}
					select {
					case body := <-requests:
						if got := gjson.GetBytes(body, "max_output_tokens").Raw; got != tc.want {
							t.Fatalf("upstream max_output_tokens = %q, want %q; body=%s", got, tc.want, body)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("upstream received no request within 10s")
					}
				})
			}
		}
	}
}
