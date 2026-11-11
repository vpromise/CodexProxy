package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func claudeFallbackResponseFixture(blocks, iterations bool) []byte {
	content := `[{"type":"text","text":"before "}`
	if blocks {
		content += `,{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-5"},"trigger":{"type":"refusal"}}`
	}
	content += `,{"type":"text","text":"after"},{"type":"tool_use","id":"toolu_fallback","name":"inspect","input":{"path":"a"}}]`
	usage := `{"input_tokens":5,"cache_read_input_tokens":3,"output_tokens":7`
	if iterations {
		usage += `,"iterations":[{"type":"message","model":"claude-opus-5-5"},{"type":"fallback_message","model":"claude-opus-5"},{"type":"fallback_message","model":"claude-opus-4-8"}]`
	}
	return []byte(`{"type":"message","id":"msg_fallback","role":"assistant","model":"claude-opus-5-5","content":` + content + `,"stop_reason":"tool_use","usage":` + usage + `}}`)
}

func assertClaudeFallbackResponse(t *testing.T, format sdktranslator.Format, body []byte, wantModel string) {
	t.Helper()
	root := gjson.ParseBytes(body)
	if got := root.Get("model").String(); got != wantModel {
		t.Errorf("model = %q, want %q", got, wantModel)
	}
	if format == sdktranslator.FormatOpenAI {
		for path, want := range map[string]string{
			"choices.0.message.content":                         "before after",
			"choices.0.message.tool_calls.0.id":                 "toolu_fallback",
			"choices.0.message.tool_calls.0.function.name":      "inspect",
			"choices.0.message.tool_calls.0.function.arguments": `{"path":"a"}`,
			"choices.0.finish_reason":                           "tool_calls",
		} {
			if got := root.Get(path).String(); got != want {
				t.Errorf("%s = %q, want %q", path, got, want)
			}
		}
		if root.Get("usage.prompt_tokens").Int() != 8 || root.Get("usage.completion_tokens").Int() != 7 {
			t.Errorf("usage changed: %s", root.Get("usage").Raw)
		}
		return
	}
	var text string
	var calls int
	for _, item := range root.Get("output").Array() {
		if item.Get("type").String() == "message" {
			for _, part := range item.Get("content").Array() {
				text += part.Get("text").String()
			}
		}
		if item.Get("type").String() == "function_call" {
			calls++
			if item.Get("call_id").String() != "toolu_fallback" || item.Get("name").String() != "inspect" || item.Get("arguments").String() != `{"path":"a"}` {
				t.Errorf("tool changed: %s", item.Raw)
			}
		}
	}
	if text != "before after" || calls != 1 || root.Get("status").String() != "completed" {
		t.Errorf("output changed: %s", body)
	}
	if root.Get("usage.input_tokens").Int() != 8 || root.Get("usage.output_tokens").Int() != 7 {
		t.Errorf("usage changed: %s", root.Get("usage").Raw)
	}
}

func TestClaudeExplicitFallbackModelNonStream(t *testing.T) {
	request := []byte(`{"model":"client-alias","messages":[{"role":"user","content":"hello"}],"input":"hello"}`)
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		for _, fixture := range []struct {
			name               string
			blocks, iterations bool
			want               string
		}{
			{"block", true, false, "claude-opus-5"},
			{"iterations", false, true, "claude-opus-4-8"},
			{"multiple_hops", true, true, "claude-opus-4-8"},
			{"no_fallback", false, false, ""},
		} {
			for _, nativeJSON := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/native_json=%t", format, fixture.name, nativeJSON), func(t *testing.T) {
					raw := claudeFallbackResponseFixture(fixture.blocks, fixture.iterations)
					if !nativeJSON {
						raw, _ = translatorcommon.ClaudeMessagesJSONToSSE(raw)
					}
					var state any
					out := sdktranslator.TranslateNonStream(context.Background(), sdktranslator.FormatClaude, format, "client-alias", request, request, raw, &state)
					want := fixture.want
					if want == "" {
						want = "claude-opus-5-5"
						if format == sdktranslator.FormatOpenAIResponse && !nativeJSON {
							want = "client-alias"
						}
					}
					assertClaudeFallbackResponse(t, format, out, want)
				})
			}
		}
	}
}

func TestClaudeExplicitFallbackModelExecute(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(string(format), func(t *testing.T) {
			raw, _ := translatorcommon.ClaudeMessagesJSONToSSE(claudeFallbackResponseFixture(true, true))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				if _, exists := body["fallbacks"]; exists {
					t.Error("unexpected fallback policy injection")
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			payload := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}],"input":"hello"}`)
			auth := &cliproxyauth.Auth{ID: "fallback-model", Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
			response, err := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: payload})
			if err != nil {
				t.Fatal(err)
			}
			assertClaudeFallbackResponse(t, format, response.Payload, "claude-opus-4-8")
		})
	}
}

func TestClaudeExplicitFallbackModelStream(t *testing.T) {
	request := []byte(`{"model":"client-alias"}`)
	// Sparse SSE reports its second fallback in final usage only.
	events := []string{
		`{"type":"message_start","message":{"id":"msg_fallback","model":"claude-opus-5-5","usage":{"input_tokens":5,"cache_read_input_tokens":3}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"before "}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"fallback","from":{"model":"claude-opus-5-5"},"to":{"model":"claude-opus-5"}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"after"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":7,"iterations":[{"type":"fallback_message","model":"claude-opus-5"},{"type":"fallback_message","model":"claude-opus-4-8"}]}}`,
		`{"type":"message_stop"}`,
	}
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(string(format), func(t *testing.T) {
			var state any
			var models []string
			var text string
			var final gjson.Result
			for _, event := range events {
				for _, chunk := range sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatClaude, format, "client-alias", request, request, []byte("data: "+event), &state) {
					for _, line := range bytes.Split(chunk, []byte("\n")) {
						line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
						if !gjson.ValidBytes(line) {
							continue
						}
						root := gjson.ParseBytes(line)
						model := root.Get("model").String()
						if format == sdktranslator.FormatOpenAIResponse {
							model = root.Get("response.model").String()
							if root.Get("type").String() == "response.output_text.delta" {
								text += root.Get("delta").String()
							}
							if root.Get("type").String() == "response.completed" {
								final = root.Get("response")
							}
						} else {
							text += root.Get("choices.0.delta.content").String()
							final = root
						}
						if model != "" && (len(models) == 0 || models[len(models)-1] != model) {
							models = append(models, model)
						}
					}
				}
			}
			if got := strings.Join(models, ","); got != "client-alias,claude-opus-5,claude-opus-4-8" {
				t.Errorf("serving model transitions = %s", got)
			}
			if text != "before after" {
				t.Errorf("text changed or duplicated: %q", text)
			}
			usagePath := "usage.completion_tokens"
			if format == sdktranslator.FormatOpenAIResponse {
				usagePath = "usage.output_tokens"
			}
			if final.Get(usagePath).Int() != 7 {
				t.Errorf("final usage changed: %s", final.Raw)
			}
		})
	}
}

func TestClaudeExplicitFallbackModelExecuteStream(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		for _, fails := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%t", format, fails), func(t *testing.T) {
				raw, _ := translatorcommon.ClaudeMessagesJSONToSSE(claudeFallbackResponseFixture(true, true))
				if fails {
					raw = bytes.Replace(raw, []byte(`data: {"type":"message_stop"}`), []byte(`data: {"type":"error","error":{"type":"overloaded_error","message":"upstream overloaded"}}`), 1)
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = w.Write(raw)
				}))
				defer server.Close()
				payload := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}],"input":"hello"}`)
				auth := &cliproxyauth.Auth{ID: "fallback-stream", Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
				result, err := NewClaudeExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: format, OriginalRequest: payload})
				if err != nil {
					t.Fatal(err)
				}
				var output bytes.Buffer
				var streamErr error
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						streamErr = chunk.Err
					}
					output.Write(chunk.Payload)
					output.WriteByte('\n')
				}
				if fails {
					if streamErr == nil || !strings.Contains(streamErr.Error(), "upstream overloaded") {
						t.Fatalf("upstream error changed: %v", streamErr)
					}
				} else if streamErr != nil {
					t.Fatal(streamErr)
				}
				var final gjson.Result
				var text, toolArgs string
				var toolCalls int
				for _, line := range bytes.Split(output.Bytes(), []byte("\n")) {
					line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
					if !gjson.ValidBytes(line) {
						continue
					}
					root := gjson.ParseBytes(line)
					if format == sdktranslator.FormatOpenAI {
						final = root
						text += root.Get("choices.0.delta.content").String()
						for _, call := range root.Get("choices.0.delta.tool_calls").Array() {
							toolCalls++
							toolArgs += call.Get("function.arguments").String()
						}
					} else {
						switch root.Get("type").String() {
						case "response.completed":
							final = root.Get("response")
						case "response.output_text.delta":
							text += root.Get("delta").String()
						case "response.function_call_arguments.done":
							toolCalls++
							toolArgs += root.Get("arguments").String()
						}
					}
				}
				if !fails {
					if final.Get("model").String() != "claude-opus-4-8" {
						t.Errorf("final model = %q", final.Get("model").String())
					}
					if toolCalls != 1 || toolArgs != `{"path":"a"}` {
						t.Errorf("tool calls changed: count=%d, args=%s", toolCalls, toolArgs)
					}
				}
				if text != "before after" {
					t.Errorf("text changed or duplicated: %q", text)
				}
			})
		}
	}
}

func TestClaudeExplicitFallbackModelStreamStateReset(t *testing.T) {
	request := []byte(`{"model":"client-alias"}`)
	for _, format := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(string(format), func(t *testing.T) {
			var state any
			convert := func(event string) [][]byte {
				return sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatClaude, format, "client-alias", request, request, []byte("data: "+event), &state)
			}
			convert(`{"type":"message_start","message":{"id":"msg_old","model":"claude-opus-5-5"}}`)
			convert(`{"type":"content_block_start","index":0,"content_block":{"type":"fallback","to":{"model":"claude-opus-5"}}}`)
			convert(`{"type":"message_stop"}`)
			chunks := convert(`{"type":"message_start","message":{"id":"msg_new","model":"claude-opus-5-5-20261101"}}`)
			for _, chunk := range chunks {
				for _, line := range bytes.Split(chunk, []byte("\n")) {
					line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
					if !gjson.ValidBytes(line) {
						continue
					}
					path := "model"
					if format == sdktranslator.FormatOpenAIResponse {
						path = "response.model"
					}
					if got := gjson.GetBytes(line, path).String(); got != "client-alias" {
						t.Errorf("new message leaked fallback model or changed alias: %s", line)
					}
				}
			}
		})
	}
}
