package responses

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesToClaudeInlinePDFMediaType(t *testing.T) {
	const pdf = "JVBERi0xLjQK"
	for _, tc := range []struct {
		name     string
		data     string
		wantData string
		wantMIME string
	}{
		{"raw PDF", pdf, pdf, "application/pdf"},
		{"generic PDF data URL", "data:application/octet-stream;base64," + pdf, pdf, "application/pdf"},
		{"explicit MIME still takes precedence", "data:application/x-example;base64," + pdf, pdf, "application/x-example"},
		{"non PDF", "aGVsbG8=", "aGVsbG8=", "application/octet-stream"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := fmt.Sprintf(`{"type":"input_file","file_data":%q}`, tc.data)
			input := `{"input":[
				{"type":"message","role":"user","content":[` + file + `]},
				{"type":"function_call","call_id":"call_1","name":"read","arguments":"{}"},
				{"type":"function_call_output","call_id":"call_1","output":[` + file + `]}]}`
			out := gjson.ParseBytes(ConvertOpenAIResponsesRequestToClaude("claude-test", []byte(input), false))
			if got := out.Get("messages.2.content.0.tool_use_id").String(); got != "call_1" {
				t.Fatalf("tool result lost its call pairing: %s", out.Raw)
			}
			for _, path := range []string{"messages.0.content.0", "messages.2.content.0.content.0"} {
				block := out.Get(path)
				if block.Get("type").String() != "document" || block.Get("source.type").String() != "base64" ||
					block.Get("source.media_type").String() != tc.wantMIME || block.Get("source.data").String() != tc.wantData {
					t.Errorf("%s = %s, want base64 document with %s", path, block.Raw, tc.wantMIME)
				}
			}
		})
	}
}
