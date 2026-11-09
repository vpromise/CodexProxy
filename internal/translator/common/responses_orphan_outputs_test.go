package common

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOrphanResponsesToolOutputsPreserveReferencesAndContent(t *testing.T) {
	cases := []struct{ name, input, path, want string }{
		{"generic item ID is not call ID", `{"input":[{"type":"function_call_output","id":"item_1","output":"x"}]}`, "input.0.type", "message"},
		{"alias reference", `{"input":[{"type":"custom_tool_call_output","tool_call_id":"call_a","input":"raw","output":"x"}]}`, "input.0.call_id", "call_a"},
		{"camel reference", `{"input":[{"type":"function_call_output","callId":"call_a","output":"x"}]}`, "input.0.call_id", "call_a"},
		{"anonymous call output stays paired", `{"input":[{"type":"function_call","name":"run"},{"type":"function_call_output","output":"x"}]}`, "input.1.type", "function_call_output"},
		{"delegation is never consumed", `{"input":[{"type":"function_call","call_id":"a","name":"create_thread"},{"type":"function_call_output","namespace":"codex_app","name":"create_thread","output":"opaque"}]}`, "input.1.call_id", ""},
		{"future explicit result reserves call", `{"input":[{"type":"function_call","call_id":"a","name":"run"},{"type":"function_call_output","name":"run","output":"context"},{"type":"message","role":"user","content":"continue"},{"type":"function_call_output","call_id":"a","output":"result"}]}`, "input.1.type", "message"},
		{"custom output matches custom call", `{"input":[{"type":"function_call","call_id":"a","name":"run"},{"type":"custom_tool_call","call_id":"b","name":"run"},{"type":"custom_tool_call_output","name":"run","output":"custom"},{"type":"function_call_output","name":"run","output":"function"}]}`, "input.2.call_id", "b"},
		{"function output matches function call", `{"input":[{"type":"function_call","call_id":"a","name":"run"},{"type":"custom_tool_call","call_id":"b","name":"run"},{"type":"custom_tool_call_output","name":"run","output":"custom"},{"type":"function_call_output","name":"run","output":"function"}]}`, "input.3.call_id", "a"},
		{"raw whitespace", `{"input":[{"type":"function_call_output","name":"code","output":"  indented\n\n"}]}`, "input.0.content.0.text", "Tool output from code:\n  indented\n\n"},
		{"image comes before later text", `{"input":[{"type":"custom_tool_call_output","output":[{"type":"input_image","image_url":"https://example.com/x.png"},{"type":"input_text","text":"after image"},{"type":"input_file","file_id":"file_1"}]}]}`, "input.0.content.2.text", "after image"},
		{"large raw integer remains precise", `{"input":[{"type":"function_call_output","output":[{"type":"future_media","n":900719925474099312345}]}]}`, "input.0.content.1.n", "900719925474099312345"},
		{"unknown primitive is retained", `{"input":[{"type":"function_call_output","output":[42]}]}`, "input.0.content.0.text", "Tool output from tool:\n42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeOrphanResponsesToolOutputs([]byte(tc.input))
			if !gjson.ValidBytes(got) {
				t.Fatalf("invalid result: %s", got)
			}
			if value := gjson.GetBytes(got, tc.path).String(); value != tc.want {
				t.Fatalf("%s=%q want %q: %s", tc.path, value, tc.want, got)
			}
			if again := SanitizeOrphanResponsesToolOutputs(got); !bytes.Equal(again, got) {
				t.Fatalf("normalization not idempotent: %s -> %s", got, again)
			}
		})
	}
}

func TestOrphanResponsesToolOutputsLeaveValidRequestsByteIdentical(t *testing.T) {
	for _, raw := range []string{
		` { "input": "hello", "model":"test" } `,
		`{"input":[{"type":"function_call","call_id":"a","name":"run","arguments":"{}"},{"type":"function_call_output","call_id":"a","output":{"n":900719925474099312345}}]}`,
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file_1"}]}]}`,
		`{"input":[{"type":"function_call_output","namespace":"codex_app","name":"send_message_to_thread","output":"opaque"}]}`,
	} {
		if got := SanitizeOrphanResponsesToolOutputs([]byte(raw)); string(got) != raw {
			t.Fatalf("valid payload changed: %s -> %s", raw, got)
		}
	}
}
