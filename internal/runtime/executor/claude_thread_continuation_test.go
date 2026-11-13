package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/sjson"
)

// Synthetic protocol diagnosis. No provider or live credentials are used.
func TestClaudeThreadThinkingChangesPreserveDeclaredBetas(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			headers := claudeNativeHelperHeaders(claudeCode2321MainBetas)
			headers.Set("User-Agent", "claude-cli/2.1.321 (external, cli)")
			headers.Set("X-Stainless-Package-Version", "0.128.0")
			headers.Set("X-Stainless-Runtime-Version", "v26.12.1")
			create := claudeIsolationNativeBody(claudeNativeHelperSessionID, "")
			create, _ = sjson.SetBytes(create, "model", "claude-sonnet-4-5")
			create, _ = sjson.SetBytes(create, "thread", map[string]string{"type": "create"})
			continued, _ := sjson.DeleteBytes(create, "system")
			continued, _ = sjson.DeleteBytes(continued, "tools")
			continued, _ = sjson.SetBytes(continued, "thread", map[string]string{"type": "continue", "previous_message_id": "msg_audit"})
			continued, _ = sjson.SetBytes(continued, "thinking", map[string]string{"type": "disabled"})
			var sent []string
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				sent = append(sent, helps.HeaderValueCaseInsensitive(req.Header, "Anthropic-Beta"))
				response := `{"id":"msg_audit","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
				contentType := "application/json"
				if mode == "stream" {
					contentType = "text/event-stream"
					response = `event: message_start
data: {"type":"message_start","message":{"id":"msg_audit","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}

event: message_stop
data: {"type":"message_stop"}

`
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := claudeNativeHelperOAuthAuth("")
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers}
			for _, body := range [][]byte{create, continued} {
				req := coreexecutor.Request{Model: "claude-sonnet-4-5", Payload: body}
				if mode == "stream" {
					result, err := executor.ExecuteStream(ctx, auth, req, opts)
					if err != nil {
						t.Fatal(err)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			if len(sent) != 2 || sent[0] != sent[1] || !strings.Contains(sent[0], claudeThinkingDisplayUpdatesBeta) || !strings.Contains(sent[1], claudeThinkingDisplayUpdatesBeta) {
				t.Fatalf("thread capabilities changed across turns, sent=%q", sent)
			}

		})
	}
}
