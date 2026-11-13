package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// This regression uses a synthetic upstream retaining
// the tool declaration from thread creation. It uses no provider credentials.
func TestClaudeReconstructedThreadContinuity(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			bodies := []string{
				`{"model":"claude-sonnet-4-5","max_tokens":1024,"thread":{"type":"create"},"tools":[{"name":"lookup_weather","input_schema":{"type":"object","properties":{}},"defer_loading":true}],"messages":[{"role":"user","content":"Use lookup_weather."}]}`,
				`{"model":"claude-sonnet-4-5","max_tokens":1024,"thread":{"type":"continue","previous_message_id":"msg_audit"},"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_audit","content":"Sunny."},{"type":"text","text":"Use lookup_weather again."}]}]}`,
			}
			auth := claudeNativeHelperOAuthAuth("")
			auth.Attributes["cloak_mode"] = "always"
			var headers []http.Header
			var results []string
			alias := ""
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				body, errRead := io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				headers = append(headers, req.Header.Clone())
				if len(headers) == 1 {
					alias = gjson.GetBytes(body, "tools.0.name").String()
					if alias == "" || alias == "lookup_weather" {
						return nil, fmt.Errorf("fixture did not enter reconstructed alias path: %q", alias)
					}
					if !gjson.GetBytes(body, "tools.0.defer_loading").Bool() {
						return nil, fmt.Errorf("fixture lost deferred-tool feature")
					}
				}
				tool := fmt.Sprintf(`{"type":"tool_use","id":"toolu_audit","name":%q,"input":{}}`, alias)
				response := fmt.Sprintf(`{"id":"msg_audit","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[%s],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, tool)
				contentType := "application/json"
				if gjson.GetBytes(body, "stream").Bool() {
					contentType = "text/event-stream"
					response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_audit\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
						"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":" + tool + "}\n\n" +
						"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
						"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":1}}\n\n" +
						"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			e := NewClaudeExecutor(&config.Config{})
			for _, raw := range bodies {
				payload := []byte(raw)
				if mode == "stream" {
					var err error
					payload, err = sjson.SetBytes(payload, "stream", true)
					if err != nil {
						t.Fatal(err)
					}
				}
				request := coreexecutor.Request{Model: "claude-sonnet-4-5", Payload: payload}
				options := coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: http.Header{}}
				if mode == "stream" {
					response, err := e.ExecuteStream(ctx, auth, request, options)
					if err != nil {
						t.Fatal(err)
					}
					var data strings.Builder
					for chunk := range response.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						data.Write(chunk.Payload)
					}
					results = append(results, data.String())
				} else {
					response, err := e.Execute(ctx, auth, request, options)
					if err != nil {
						t.Fatal(err)
					}
					results = append(results, string(response.Payload))
				}
			}
			if len(headers) != 2 || len(results) != 2 {
				t.Fatalf("expected two successful calls, headers=%d results=%d", len(headers), len(results))
			}
			first := helps.HeaderValueCaseInsensitive(headers[0], "Anthropic-Beta")
			second := helps.HeaderValueCaseInsensitive(headers[1], "Anthropic-Beta")
			t.Logf("create betas=%s", first)
			t.Logf("continue betas=%s", second)
			if !strings.Contains(first, claudeAdvancedToolUseBeta) {
				t.Fatal("fixture did not activate advanced-tool-use")
			}
			if !strings.Contains(second, claudeAdvancedToolUseBeta) {
				t.Error("continuation lost advanced-tool-use for the server-retained deferred tool")
			}
			if strings.Contains(results[0], alias) || !strings.Contains(results[0], "lookup_weather") {
				t.Fatalf("create response failed its control assertion: %s", results[0])
			}
			if strings.Contains(results[1], alias) {
				t.Errorf("continuation leaked generated tool alias instead of caller name: %s", results[1])
			}
		})
	}
}
