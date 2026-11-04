package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexTokenLimitsAcrossSourceFormats(t *testing.T) {
	for _, tc := range []struct {
		name, format, payload string
		want                  int64
		exists                bool
	}{
		{"responses explicit", "openai-response", `{"input":"hi","max_output_tokens":41,"max_completion_tokens":42,"max_tokens":43}`, 41, true},
		{"responses legacy", "openai-response", `{"input":"hi","max_completion_tokens":42,"max_tokens":43}`, 42, true},
		{"responses null fallback", "openai-response", `{"input":"hi","max_output_tokens":null,"max_tokens":43}`, 43, true},
		{"responses absent", "openai-response", `{"input":"hi"}`, 0, false},
		{"chat precedence", "openai", `{"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":42,"max_tokens":43,"max_output_tokens":44}`, 42, true},
		{"chat legacy", "openai", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":43}`, 43, true},
		{"chat fallback", "openai", `{"messages":[{"role":"user","content":"hi"}],"max_output_tokens":44}`, 44, true},
		{"chat invalid skipped", "openai", `{"messages":[{"role":"user","content":"hi"}],"max_completion_tokens":"bad","max_tokens":43}`, 43, true},
		{"claude precedence", "claude", `{"messages":[{"role":"user","content":"hi"}],"max_tokens":43,"max_output_tokens":44}`, 43, true},
		{"claude fallback", "claude", `{"messages":[{"role":"user","content":"hi"}],"max_output_tokens":44}`, 44, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := sdktranslator.TranslateRequestChecked(sdktranslator.FromString(tc.format), sdktranslator.FromString("codex"), "gpt-6-astra", []byte(tc.payload), true)
			if err != nil {
				t.Fatal(err)
			}
			got := gjson.GetBytes(out, "max_output_tokens")
			if got.Exists() != tc.exists || (tc.exists && got.Int() != tc.want) {
				t.Fatalf("limit=%s want %d; body=%s", got.Raw, tc.want, out)
			}
			for _, key := range []string{"max_tokens", "max_completion_tokens"} {
				if gjson.GetBytes(out, key).Exists() {
					t.Fatalf("legacy key %s leaked: %s", key, out)
				}
			}
		})
	}
}

const codexTokenLimitCompleted = `{"type":"response.completed","response":{"id":"resp_limit","object":"response","status":"completed","model":"gpt-6-astra","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`

func TestCodexTokenLimitReachesHTTPExecution(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "compact"} {
		t.Run(mode, func(t *testing.T) {
			bodies := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				bodies <- body
				if mode == "compact" {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"id":"compact_1","object":"response.compaction","output":[]}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+codexTokenLimitCompleted+"\n\n")
			}))
			defer server.Close()
			e := NewCodexExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture"}}
			req := cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":"hi","max_output_tokens":37}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			if mode == "stream" {
				opts.Stream = true
				result, err := e.ExecuteStream(context.Background(), auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for c := range result.Chunks {
					if c.Err != nil {
						t.Fatal(c.Err)
					}
				}
			} else {
				if mode == "compact" {
					opts.Alt = "responses/compact"
				}
				if _, err := e.Execute(context.Background(), auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			body := <-bodies
			if got := gjson.GetBytes(body, "max_output_tokens"); got.Int() != 37 {
				t.Fatalf("upstream max_output_tokens=%s; body=%s", got.Raw, body)
			}
		})
	}
}

func TestCodexTokenLimitReachesWebSocketExecution(t *testing.T) {
	bodies := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, body, err := conn.ReadMessage()
		if err != nil {
			t.Error(err)
			return
		}
		bodies <- body
		if err := conn.WriteMessage(websocket.TextMessage, []byte(codexTokenLimitCompleted)); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	e := NewCodexWebsocketsExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{ID: "limit-fixture", Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture"}}
	_, err := e.Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "gpt-6-astra", Payload: []byte(`{"input":"hi","max_output_tokens":37}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	if body := <-bodies; gjson.GetBytes(body, "max_output_tokens").Int() != 37 {
		t.Fatalf("websocket limit missing: %s", body)
	}
}
