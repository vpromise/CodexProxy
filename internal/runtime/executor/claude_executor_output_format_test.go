package executor

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutorNativeOutputFormat(t *testing.T) {
	const schema = `{"type":"object","properties":{"n":{"type":"integer","const":9007199254740993}},"required":["n"],"additionalProperties":false}`
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude} {
		for _, credential := range []string{"api-key", "oauth"} {
			for _, cloak := range []string{"never", "auto", "always"} {
				for _, mode := range []string{"execute", "stream", "native-count", "local-count"} {
					t.Run(source.String()+"/"+credential+"/"+cloak+"/"+mode, func(t *testing.T) {
						type captured struct {
							body    []byte
							headers http.Header
							length  int64
						}
						capturedRequest := make(chan captured, 1)
						server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							body, err := io.ReadAll(r.Body)
							if err != nil {
								t.Error(err)
							}
							capturedRequest <- captured{body, r.Header.Clone(), r.ContentLength}
							if strings.Contains(r.URL.Path, "count_tokens") {
								w.Header().Set("Content-Type", "application/json")
								_, _ = w.Write([]byte(`{"input_tokens":7}`))
								return
							}
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_fmt\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"{}\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
						}))
						defer server.Close()
						key := "sk-ant-api-fixture"
						if credential == "oauth" {
							key = "sk-ant-oat-fixture"
						}
						auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "claude", Attributes: map[string]string{"api_key": key, "base_url": server.URL, "cloak_mode": cloak}}
						if credential == "oauth" {
							auth.Metadata = claudeOAuthTestMetadata()
						}
						cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: key, Cloak: &config.CloakConfig{Mode: cloak}}}}
						payload := `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"response_format":{"type":"json_schema","json_schema":{"schema":` + schema + `}}}`
						if source == sdktranslator.FormatOpenAIResponse {
							payload = `{"model":"claude-sonnet-4-6","input":"hello","text":{"format":{"type":"json_schema","schema":` + schema + `}}}`
						}
						if source == sdktranslator.FormatClaude {
							payload = `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}],"output_config":{"format":{"type":"json_schema","schema":` + schema + `}}}`
						}
						req := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: []byte(payload)}
						opts := cliproxyexecutor.Options{SourceFormat: source, OriginalRequest: req.Payload}
						ex := NewClaudeExecutor(cfg)
						var err error
						switch mode {
						case "execute":
							_, err = ex.Execute(t.Context(), auth, req, opts)
						case "stream":
							var result *cliproxyexecutor.StreamResult
							result, err = ex.ExecuteStream(t.Context(), auth, req, opts)
							if result != nil {
								for chunk := range result.Chunks {
									if chunk.Err != nil {
										t.Fatal(chunk.Err)
									}
								}
							}
						case "native-count":
							_, err = ex.countTokensUpstream(t.Context(), auth, req, opts)
						case "local-count":
							_, err = ex.CountTokens(t.Context(), auth, req, opts)
						}
						if err != nil {
							t.Fatal(err)
						}
						if mode == "local-count" {
							if len(capturedRequest) != 0 {
								t.Fatal("custom gateway count unexpectedly dispatched")
							}
							return
						}
						got := <-capturedRequest
						if gjson.GetBytes(got.body, "output_config.format.schema").Raw != schema {
							t.Fatalf("wire schema changed: %s", got.body)
						}
						if got.length != int64(len(got.body)) {
							t.Fatalf("content length %d differs from %d", got.length, len(got.body))
						}
						if mode != "native-count" {
							hasBeta := strings.Contains(got.headers.Get("Anthropic-Beta"), claudeStructuredOutputsBeta)
							wantBeta := credential == "oauth" || cloak != "never"
							if hasBeta != wantBeta {
								t.Fatalf("structured-output beta = %t, want %t: %v", hasBeta, wantBeta, got.headers)
							}
						}
					})
				}
			}
		}
	}
}
