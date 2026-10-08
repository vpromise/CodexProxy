package responses

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesAttachmentsSurviveChatTranslation(t *testing.T) {
	for _, tc := range []struct{ name, part, expected string }{
		{"file_id", `{"type":"input_file","file_id":"file-fixture","filename":"report.pdf"}`, `{"type":"file","file":{"file_id":"file-fixture","filename":"report.pdf"}}`},
		{"file_data", `{"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0=","filename":"report.pdf"}`, `{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0=","filename":"report.pdf"}}`},
		{"flat_audio", `{"type":"input_audio","data":"YXVkaW8=","format":"wav"}`, `{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"wav"}}`},
		{"nested_audio", `{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"mp3"}}`, `{"type":"input_audio","input_audio":{"data":"YXVkaW8=","format":"mp3"}}`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				raw := []byte(`{"model":"source","input":[{"role":"user","content":[{"type":"input_text","text":"before"},` + tc.part + `,{"type":"input_text","text":"after"}]}]}`)
				out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("target", raw, stream)
				parts := gjson.GetBytes(out, "messages.0.content").Array()
				if len(parts) != 3 {
					t.Fatalf("lost attachment: %s", out)
				}
				if parts[0].Get("text").String() != "before" || parts[2].Get("text").String() != "after" {
					t.Fatalf("changed content order: %s", out)
				}
				if parts[1].Raw != tc.expected {
					t.Fatalf("attachment = %s, want %s", parts[1].Raw, tc.expected)
				}
			})
		}
	}
}
