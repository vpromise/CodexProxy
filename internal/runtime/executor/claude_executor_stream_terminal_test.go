package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type claudeTerminalUsageCaptureKey struct{}
type claudeTerminalUsageCapturePlugin struct{}

func (claudeTerminalUsageCapturePlugin) HandleUsage(ctx context.Context, record usage.Record) {
	if records, ok := ctx.Value(claudeTerminalUsageCaptureKey{}).(chan usage.Record); ok {
		select {
		case records <- record:
		default:
		}
	}
}

func TestClaudeExecutorExecuteStreamCompletesAtTerminalEvent(t *testing.T) {
	usage.RegisterNamedPlugin(t.Name(), claudeTerminalUsageCapturePlugin{})
	const streamData = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_terminal","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","usage":{"input_tokens":100,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	for _, tc := range []struct {
		name     string
		format   sdktranslator.Format
		payload  string
		terminal string
	}{
		{
			name:     "passthrough",
			format:   sdktranslator.FormatClaude,
			payload:  `{"model":"claude-opus-4-6","max_tokens":20,"messages":[{"role":"user","content":"hi"}],"stream":true}`,
			terminal: `"type":"message_stop"`,
		},
		{
			name:     "responses",
			format:   sdktranslator.FormatOpenAIResponse,
			payload:  `{"model":"claude-opus-4-6","input":"hi","stream":true}`,
			terminal: `"type":"response.completed"`,
		},
	} {
		for _, disconnect := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/disconnect=%t", tc.name, disconnect), func(t *testing.T) {
				upstreamRelease := make(chan struct{})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write([]byte(streamData))
					w.(http.Flusher).Flush()
					// Completion must not depend on the upstream closing its body.
					select {
					case <-r.Context().Done():
					case <-upstreamRelease:
					}
				}))
				defer func() {
					close(upstreamRelease)
					server.Close()
				}()

				records := make(chan usage.Record, 4)
				ctx := context.WithValue(context.Background(), claudeTerminalUsageCaptureKey{}, records)
				ctx, cancel := context.WithCancel(ctx)
				defer cancel()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				executor := NewClaudeExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "terminal-test", "base_url": server.URL}}
				result, errStream := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
					Model:   "claude-opus-4-6",
					Payload: []byte(tc.payload),
				}, cliproxyexecutor.Options{SourceFormat: tc.format, ResponseFormat: tc.format})
				if errStream != nil {
					t.Fatalf("ExecuteStream: %v", errStream)
				}

				terminalCount := 0
			readLoop:
				for {
					select {
					case chunk, ok := <-result.Chunks:
						if !ok {
							break readLoop
						}
						if chunk.Err != nil {
							t.Fatalf("completed stream returned an error: %v", chunk.Err)
						}
						if strings.Contains(string(chunk.Payload), tc.terminal) {
							terminalCount++
							if disconnect {
								// The client may disconnect as soon as it receives completion.
								cancel()
							}
						}
					case <-deadline.C:
						t.Fatal("stream did not close after the terminal event")
					}
				}
				if terminalCount != 1 {
					t.Fatalf("terminal event count = %d, want 1", terminalCount)
				}
				select {
				case record := <-records:
					if record.Failed {
						t.Fatalf("completed stream recorded failure: %+v", record.Fail)
					}
					if record.Detail.InputTokens != 100 || record.Detail.OutputTokens != 15 {
						t.Fatalf("usage = %+v, want input=100 output=15", record.Detail)
					}
				case <-deadline.C:
					t.Fatal("usage record not delivered")
				}
			})
		}
	}
}
