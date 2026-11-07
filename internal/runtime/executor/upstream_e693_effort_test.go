package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorClaudeStandaloneEffortFinalPayload(t *testing.T) {
	models := []config.PayloadModelRule{{Name: "custom-openai", Protocol: "openai"}}
	for _, tt := range []struct {
		name       string
		payload    config.PayloadConfig
		wantEffort string
	}{
		{name: "standalone effort preserved", wantEffort: "high"},
		{
			name: "final override wins",
			payload: config.PayloadConfig{Override: []config.PayloadRule{
				{Models: models, Params: map[string]any{"reasoning_effort": "low"}},
			}},
			wantEffort: "low",
		},
		{
			name: "final filter is not restored",
			payload: config.PayloadConfig{Filter: []config.PayloadFilterRule{
				{Models: models, Params: []string{"reasoning_effort"}},
			}},
		},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", tt.name, stream), func(t *testing.T) {
				bodies := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read upstream request: %v", errRead)
					}
					bodies <- body
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`)
				}))
				t.Cleanup(server.Close)

				executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{Payload: tt.payload})
				auth := &cliproxyauth.Auth{
					Provider: "openai-compatibility",
					Attributes: map[string]string{
						"base_url": server.URL,
						"api_key":  "test-key",
					},
				}
				req := cliproxyexecutor.Request{
					Model:   "custom-openai",
					Payload: []byte(`{"model":"custom-openai","max_tokens":16,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`),
				}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
				if stream {
					result, errStream := executor.ExecuteStream(context.Background(), auth, req, opts)
					if errStream != nil {
						t.Fatalf("ExecuteStream: %v", errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk: %v", chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
					t.Fatalf("Execute: %v", errExecute)
				}
				body := <-bodies
				effort := gjson.GetBytes(body, "reasoning_effort")
				if effort.String() != tt.wantEffort || (tt.wantEffort == "" && effort.Exists()) {
					t.Fatalf("upstream reasoning_effort = %s, want %q; body=%s", effort.Raw, tt.wantEffort, body)
				}
			})
		}
	}
}
