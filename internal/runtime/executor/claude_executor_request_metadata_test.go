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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClaudeExecutorNative321RequestMetadata(t *testing.T) {
	const promptID = "11111111-2222-4333-8444-555555555555"
	const userID = `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"22222222-2222-4333-8444-555555555555"}`
	payload := []byte(`{"model":"claude-sonnet-5-5","max_tokens":256,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.321.abc; cc_entrypoint=sdk-cli; cch=00000; cc_prompt_id=` + promptID + `;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	for _, mode := range []string{"auto", "never", "always"} {
		for _, entry := range []string{"execute", "stream", "count"} {
			t.Run(mode+"/"+entry, func(t *testing.T) {
				headers := http.Header{
					"User-Agent": {"claude-cli/2.1.321 (external, sdk-cli)"},
					"X-App":      {"cli"}, "X-Stainless-Package-Version": {"0.128.0"},
					"X-Stainless-Runtime-Version":       {"v26.12.1"},
					"Anthropic-Beta":                    {"claude-code-20250219,interleaved-thinking-2025-05-14"},
					"X-Claude-Code-Prompt-Id":           {promptID},
					"X-Claude-Code-Request-Class":       {"main"},
					"X-Claude-Code-Sandy-Plover":        {`[{"name":"Bash"}]`},
					"X-Claude-Code-Prev-Tool-Durations": {"Bash=19"},
				}
				if !helps.DetectClaudeCodeRequest(headers, payload, false, &config.Config{}).Confirmed {
					t.Fatal("fixture is not recognized as native Claude Code")
				}
				auth := &cliproxyauth.Auth{ID: "native-metadata-" + t.Name(), Attributes: map[string]string{"api_key": "sk-ant-oat01-synthetic", "cloak_mode": mode}, Metadata: claudeOAuthTestMetadata()}
				calls := 0
				transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					body, err := io.ReadAll(request.Body)
					if err != nil {
						return nil, err
					}
					if got := helps.HeaderValueCaseInsensitive(request.Header, "X-Claude-Code-Request-Class"); got != "main" {
						t.Errorf("request class = %q", got)
					}
					if got := helps.HeaderValueCaseInsensitive(request.Header, "X-Claude-Code-Prev-Tool-Durations"); got != "Bash=19" {
						t.Errorf("previous tool duration = %q", got)
					}
					if entry != "count" {
						_, billingPrompt := helps.ExtractClaudeBillingTags(body)
						got := helps.HeaderValueCaseInsensitive(request.Header, "X-Claude-Code-Prompt-Id")
						if billingPrompt == "" || got != billingPrompt {
							t.Errorf("prompt header %q differs from billing %q", got, billingPrompt)
						}
						if got := helps.HeaderValueCaseInsensitive(request.Header, "X-Claude-Code-Sandy-Plover"); got != `[{"name":"Bash"}]` {
							t.Errorf("native tool metadata = %q", got)
						}
					}
					response := `{"id":"msg_metadata","type":"message","role":"assistant","model":"claude-sonnet-5-5","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
					contentType := "application/json"
					if entry == "stream" {
						contentType = "text/event-stream"
						response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + response + "}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
					} else if entry == "count" {
						response = `{"input_tokens":25}`
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: request}, nil
				})
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
				executor := NewClaudeExecutor(&config.Config{})
				req := cliproxyexecutor.Request{Model: "claude-sonnet-5-5", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: headers}
				var err error
				switch entry {
				case "stream":
					var result *cliproxyexecutor.StreamResult
					result, err = executor.ExecuteStream(ctx, auth, req, opts)
					if err == nil {
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					}
				case "count":
					_, err = executor.CountTokens(ctx, auth, req, opts)
				default:
					_, err = executor.Execute(ctx, auth, req, opts)
				}
				if err != nil {
					t.Fatal(err)
				}
				if calls != 1 {
					t.Fatalf("upstream calls = %d", calls)
				}
			})
		}
	}
}
