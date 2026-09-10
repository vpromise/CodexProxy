package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type claudeUsageCaptureKey struct{}
type claudeUsageCapturePlugin struct{}

func (claudeUsageCapturePlugin) HandleUsage(ctx context.Context, record usage.Record) {
	if records, ok := ctx.Value(claudeUsageCaptureKey{}).(chan usage.Record); ok {
		select {
		case records <- record:
		default:
		}
	}
}

func TestClaudeExecutorPublishesAccumulatedUsageOnCompletionOrFailure(t *testing.T) {
	usage.RegisterNamedPlugin(t.Name(), claudeUsageCapturePlugin{})
	for _, tt := range []struct {
		name   string
		stream bool
		format sdktranslator.Format
	}{
		{"Claude passthrough", true, sdktranslator.FormatClaude},
		{"OpenAI stream", true, sdktranslator.FormatOpenAI},
		{"OpenAI non-stream", false, sdktranslator.FormatOpenAI},
	} {
		for _, completed := range []bool{true, false} {
			name := tt.name + "/incomplete"
			if completed {
				name = tt.name + "/completed"
			}
			t.Run(name, func(t *testing.T) {
				sse := "data: " + `{"type":"message_start","message":{"id":"msg_usage","type":"message","role":"assistant","model":"claude-opus-4-6","content":[],"usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":30,"output_tokens":1}}}` + "\n\n" +
					"data: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":12,"output_tokens_details":{"thinking_tokens":5}}}` + "\n\n"
				if completed {
					sse += "data: " + `{"type":"message_stop"}` + "\n\n"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(sse))
				}))
				defer server.Close()
				records := make(chan usage.Record, 4)
				ctx := context.WithValue(context.Background(), claudeUsageCaptureKey{}, records)
				executor := NewClaudeExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "usage-test", "base_url": server.URL}}
				req := cliproxyexecutor.Request{Model: "claude-opus-4-6", Payload: []byte(`{"model":"claude-opus-4-6","max_tokens":20,"messages":[{"role":"user","content":"hello"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: tt.format, ResponseFormat: tt.format}
				var requestErr error
				if tt.stream {
					result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
					requestErr = errStream
					if result != nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								requestErr = chunk.Err
							}
						}
					}
				} else {
					_, requestErr = executor.Execute(ctx, auth, req, opts)
				}
				if (requestErr == nil) != completed {
					t.Fatalf("request error = %v, completed = %v", requestErr, completed)
				}
				select {
				case record := <-records:
					if record.Failed == completed {
						t.Fatalf("usage outcome failed = %v, completed = %v", record.Failed, completed)
					}
					d := record.Detail
					if d.InputTokens != 10 || d.OutputTokens != 12 || d.ReasoningTokens != 5 || d.CacheReadTokens != 20 || d.CacheCreationTokens != 30 || d.TotalTokens != 72 {
						t.Fatalf("accumulated usage = %+v", d)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("usage record not delivered")
				}
			})
		}
	}
}
