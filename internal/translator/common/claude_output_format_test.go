package common_test

import (
	"strings"
	"testing"

	chat "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/chat-completions"
	responses "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	"github.com/tidwall/gjson"
)

func TestClaudeNativeOutputFormatTranslation(t *testing.T) {
	const schema = `{"type":"object","properties":{"n":{"type":["integer","null"],"enum":[9007199254740993,9223372036854775807]},"f":{"const":0.1234567890123456789}},"additionalProperties":false,"required":["n"]}`
	for _, tc := range []struct {
		name, body string
		convert    func(string, []byte, bool) ([]byte, error)
	}{
		{"chat", `{"reasoning_effort":"high","messages":[{"role":"system","content":"caller system"},{"role":"user","content":"hello"}],"response_format":{"type":"json_schema","json_schema":{"name":"result","description":"keep this description","schema":` + schema + `}}}`, chat.ConvertOpenAIRequestToClaudeChecked},
		{"responses", `{"reasoning":{"effort":"high"},"instructions":"caller system","input":"hello","text":{"format":{"type":"json_schema","name":"result","description":"keep this description","schema":` + schema + `}},"response_format":{"type":"json_object"}}`, responses.ConvertOpenAIResponsesRequestToClaudeChecked},
		{"responses legacy spelling", `{"reasoning":{"effort":"high"},"input":"hello","response_format":{"type":"json_schema","json_schema":{"schema":` + schema + `}}}`, responses.ConvertOpenAIResponsesRequestToClaudeChecked},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/normal", true: "/stream"}[stream], func(t *testing.T) {
				out, err := tc.convert("claude-sonnet-4-6", []byte(tc.body), stream)
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(out, "output_config.format.type").String() != "json_schema" || gjson.GetBytes(out, "output_config.format.schema").Raw != schema {
					t.Fatalf("lost native schema or precision: %s", out)
				}
				if gjson.GetBytes(out, "output_config.effort").String() != "high" {
					t.Fatalf("lost thinking effort: %s", out)
				}
				system := gjson.GetBytes(out, "system").Raw
				if strings.Contains(system, "9007199254740993") {
					t.Fatalf("schema duplicated into prompt: %s", system)
				}
				if strings.Contains(tc.body, "keep this description") && (!strings.Contains(system, "keep this description") || !strings.Contains(system, "caller system")) {
					t.Fatalf("lost system/description: %s", system)
				}
			})
		}
	}
}

func TestClaudeNativeOutputFormatBoundaries(t *testing.T) {
	for _, model := range []string{"claude-3-7-sonnet-latest", "claude-opus-4-1-20250805", "claude-sonnet-4-latest", "pool/claude-sonnet-4@revision", "claude-opus-4-1-thinking", "claude-opus-4-1(high)"} {
		out := chat.ConvertOpenAIRequestToClaude(model, []byte(`{"messages":[{"role":"user","content":"hello"}],"response_format":{"type":"json_schema","json_schema":{"schema":{"type":"object"}}}}`), false)
		if gjson.GetBytes(out, "output_config.format").Exists() {
			t.Fatalf("legacy model %s gained unsupported format: %s", model, out)
		}
	}
	for _, format := range []string{`{"type":"json_object"}`, `{"type":"text"}`, `{"type":"json_schema","schema":[]}`, `{"type":"json_schema","schema":null}`, `{"type":"json_schema","json_schema":{"schema":null},"schema":{"type":"object"}}`} {
		out := chat.ConvertOpenAIRequestToClaude("claude-sonnet-4-6", []byte(`{"messages":[{"role":"user","content":"hello"}],"response_format":`+format+`}`), false)
		if gjson.GetBytes(out, "output_config.format").Exists() || gjson.GetBytes(out, "system").Exists() {
			t.Fatalf("invalid/non-schema format changed behavior: %s", out)
		}
	}
	out := responses.ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-6", []byte(`{"input":"hello","text":{"format":{"type":"text"}},"response_format":{"type":"json_schema","schema":{"type":"object"}}}`), false)
	if gjson.GetBytes(out, "output_config.format").Exists() {
		t.Fatalf("text.format precedence lost: %s", out)
	}
	for _, tc := range []struct {
		body    string
		convert func(string, []byte, bool) ([]byte, error)
	}{
		{`{"messages":[{"role":"system","content":"safe"},{"role":"user","content":[{"type":"text","text":" "},{"type":"file","file":{"file_id":"opaque"}}]}],"response_format":{"type":"json_schema","json_schema":{"description":"not a user turn","schema":{"type":"object"}}}}`, chat.ConvertOpenAIRequestToClaudeChecked},
		{`{"input":[{"role":"user","content":[{"type":"input_text","text":" "},{"type":"input_file","file_id":"opaque"}]}],"text":{"format":{"type":"json_schema","description":"not a user turn","schema":{"type":"object"}}}}`, responses.ConvertOpenAIResponsesRequestToClaudeChecked},
	} {
		if out, err := tc.convert("claude-sonnet-4-6", []byte(tc.body), false); err == nil {
			t.Fatalf("format concealed lost attachment: %s", out)
		}
	}
}
