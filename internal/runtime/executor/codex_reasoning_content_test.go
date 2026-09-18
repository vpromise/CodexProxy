package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexReasoningContentRequestScope(t *testing.T) {
	for _, mode := range []string{"http", "http-stream", "compact", "websocket", "websocket-stream", "compat-compact"} {
		t.Run(mode, func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(mode, "websocket") {
					upgrader := websocket.Upgrader{}
					conn, errUpgrade := upgrader.Upgrade(w, r, nil)
					if errUpgrade != nil {
						t.Error(errUpgrade)
						return
					}
					defer func() { _ = conn.Close() }()
					_, body, errRead := conn.ReadMessage()
					if errRead != nil {
						t.Error(errRead)
						return
					}
					captured <- body
					_ = conn.WriteMessage(websocket.TextMessage, []byte(codexCompletedEventBody))
					return
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					return
				}
				captured <- body
				if strings.HasSuffix(mode, "compact") {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`))
				} else {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte("data: " + codexCompletedEventBody + "\n\n"))
				}
			}))
			defer server.Close()
			valid := validOpenAIResponsesReasoningEncryptedContentForTest()
			payload := []byte(`{"model":"gpt-5.6-terra","input":[{"type":"reasoning","id":"rs_1","encrypted_content":"` + valid + `","summary":[],"content":[{"type":"reasoning_text","text":"old provider thought"}]},{"role":"user","content":"continue"}]}`)
			req := cliproxyexecutor.Request{Model: "gpt-5.6-terra", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
			cfg := &config.Config{Codex: config.CodexConfig{DisableCodexCloaking: true}}
			auth := codexTestAuth(server.URL)
			if strings.HasSuffix(mode, "compact") {
				opts.Alt = "responses/compact"
			}
			execute := NewCodexExecutor(cfg).Execute
			executeStream := NewCodexExecutor(cfg).ExecuteStream
			if strings.HasPrefix(mode, "websocket") {
				execute = NewCodexWebsocketsExecutor(cfg).Execute
				executeStream = NewCodexWebsocketsExecutor(cfg).ExecuteStream
			}
			if mode == "compat-compact" {
				execute = NewOpenAICompatExecutor("openai-compatibility", cfg).Execute
			}
			if strings.HasSuffix(mode, "-stream") {
				opts.Stream = true
				result, errStream := executeStream(context.Background(), auth, req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExecute := execute(context.Background(), auth, req, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			body := <-captured
			item := gjson.GetBytes(body, "input.0")
			if item.Get("encrypted_content").String() != valid || item.Get("id").String() != "rs_1" {
				t.Fatalf("reasoning replay state changed: %s", body)
			}
			if mode == "compat-compact" {
				if item.Get("content.0.text").String() != "old provider thought" || item.Get("summary").Raw != "[]" {
					t.Fatalf("generic backend received Codex-only normalization: %s", body)
				}
			} else if item.Get("content").Raw != "[]" || item.Get("summary.0.text").String() != "old provider thought" {
				t.Fatalf("Codex received unsupported cleartext content: %s", body)
			}
		})
	}
}
