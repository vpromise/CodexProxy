package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToCodexSanitizesOrphanToolOutputs(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5.4",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call_output","id":"fco_1","name":"codex_apps_open_page","namespace":"agent","output":"{\"page_id\":null}"},
			{"type":"custom_tool_call_output","id":"fco_2","name":"custom_ctx","output":[{"type":"input_text","text":"part one"},{"type":"input_text","text":"part two"},{"type":"input_image","image_url":"https://example.com/page.png"}]},
			{"type":"function_call","call_id":"call_ok","name":"shell","arguments":""},
			{"type":"function_call_output","call_id":"call_ok","output":"ok"},
			{"type":"function_call_output","call_id":" ","output":"blank id"},
			{"type":"function_call_output","id":"fco_3","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"},
			{"type":"function_call","call_id":"call_pair","name":"shell","arguments":"{}"},
			{"type":"function_call_output","output":"paired without call_id"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToCodex("gpt-5.4", raw, false)
	input := gjson.GetBytes(out, "input").Array()
	if len(input) != 9 {
		t.Fatalf("input len = %d, want 9", len(input))
	}

	if got := input[1].Get("type").String(); got != "message" {
		t.Fatalf("input[1].type = %q, want %q", got, "message")
	}
	if got := input[1].Get("role").String(); got != "user" {
		t.Fatalf("input[1].role = %q, want %q", got, "user")
	}
	want1 := "Tool output from codex_apps_open_page:\n{\"page_id\":null}"
	if got := input[1].Get("content.0.text").String(); got != want1 {
		t.Fatalf("input[1].content.0.text = %q, want %q", got, want1)
	}

	if got := input[2].Get("type").String(); got != "message" {
		t.Fatalf("input[2].type = %q, want %q", got, "message")
	}
	want2 := "Tool output from custom_ctx:\npart one\npart two"
	if got := input[2].Get("content.0.text").String(); got != want2 {
		t.Fatalf("input[2].content.0.text = %q, want %q", got, want2)
	}
	if got := input[2].Get("content.1.type").String(); got != "input_image" {
		t.Fatalf("input[2].content.1.type = %q, want %q", got, "input_image")
	}
	if got := input[2].Get("content.1.image_url").String(); got != "https://example.com/page.png" {
		t.Fatalf("input[2].content.1.image_url = %q, want %q", got, "https://example.com/page.png")
	}

	if got := input[3].Get("arguments").String(); got != "{}" {
		t.Fatalf("input[3].arguments = %q, want %q", got, "{}")
	}
	if got := input[4].Get("type").String(); got != "function_call_output" {
		t.Fatalf("input[4].type = %q, want %q", got, "function_call_output")
	}
	if got := input[4].Get("call_id").String(); got != "call_ok" {
		t.Fatalf("input[4].call_id = %q, want %q", got, "call_ok")
	}

	if got := input[5].Get("type").String(); got != "message" {
		t.Fatalf("input[5].type = %q, want %q", got, "message")
	}
	want5 := "Tool output from tool:\nblank id"
	if got := input[5].Get("content.0.text").String(); got != want5 {
		t.Fatalf("input[5].content.0.text = %q, want %q", got, want5)
	}

	if got := input[6].Get("type").String(); got != "function_call_output" {
		t.Fatalf("input[6].type = %q, want %q", got, "function_call_output")
	}
	if got := input[6].Get("namespace").String(); got != "codex_app" {
		t.Fatalf("input[6].namespace = %q, want %q", got, "codex_app")
	}

	if got := input[8].Get("type").String(); got != "function_call_output" {
		t.Fatalf("input[8].type = %q, want %q", got, "function_call_output")
	}
	if got := input[8].Get("call_id").String(); got != "call_pair" {
		t.Fatalf("input[8].call_id = %q, want %q", got, "call_pair")
	}
}
