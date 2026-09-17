package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpenAIResponsesOrphanCustomOutputPreservesPairingAndImages(t *testing.T) {
	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call_1","name":"tool","input":"command"},{"type":"custom_tool_call_output","call_id":"orphan","output":[{"type":"input_text","text":"task context"},{"type":"input_image","image_url":"https://example.invalid/image.png"}]},{"type":"custom_tool_call_output","call_id":"call_1","output":"real result"}]}`)
	got := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("fixture-model", raw, false)
	messages := gjson.GetBytes(got, "messages").Array()
	if len(messages) != 3 || messages[0].Get("role").String() != "assistant" || messages[1].Get("role").String() != "tool" || messages[1].Get("tool_call_id").String() != "call_1" || messages[1].Get("content").String() != "real result" {
		t.Fatalf("orphan interrupted the real call/result pair: %s", got)
	}
	if messages[2].Get("role").String() != "user" || messages[2].Get("content.0.text").String() != "task context" || messages[2].Get("content.1.image_url.url").String() != "https://example.invalid/image.png" {
		t.Fatalf("orphan lost task context or image: %s", got)
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_OrphanFunctionCallOutputBecomesUserMessage(t *testing.T) {
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"role":"user","content":[{"type":"input_text","text":"Task initialization"}]},
			{"type":"function_call_output","id":"fco_01a09fca-8d33-73a1-97fd-4d83ecc02f9d","name":"send_message_to_thread","output":"<codex_delegation>\n  <source_thread_id>01a022d7-d4d0-72b2-8571-4590484ccaee</source_thread_id>\n  <input>Execute sub-task</input>\n</codex_delegation>"},
			{"type":"function_call","call_id":"call_1789387253098037589_85","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1789387253098037589_85","id":"fco_01a09fca-a5f0-7b40-9943-21fbc923c537","output":"/Users/developer"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	delegationFound := false
	bashToolFound := false
	for _, message := range messages {
		role := message.Get("role").String()
		if role == "tool" && strings.TrimSpace(message.Get("tool_call_id").String()) == "" {
			t.Fatalf("orphan output emitted as tool message with empty tool_call_id: %s", string(out))
		}
		if role == "user" && strings.Contains(message.Get("content").String(), "<codex_delegation>") {
			delegationFound = true
		}
		if role == "tool" && message.Get("tool_call_id").String() == "call_1789387253098037589_85" {
			bashToolFound = true
			if got := message.Get("content").String(); got != "/Users/developer" {
				t.Fatalf("bash tool content = %q, want /Users/developer; output=%s", got, string(out))
			}
		}
	}
	if !delegationFound {
		t.Fatalf("expected orphan send_message_to_thread output as user content; output=%s", string(out))
	}
	if !bashToolFound {
		t.Fatalf("expected paired Bash tool message; output=%s", string(out))
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_UnpairedExplicitCallIDBecomesUserMessage(t *testing.T) {
	inputJSON := []byte(`{
		"model": "deepseek-v4.1-flash",
		"input": [
			{"role":"user","content":[{"type":"input_text","text":"Task initialization"}]},
			{"type":"function_call_output","call_id":"call_missing","name":"send_message_to_thread","output":"<codex_delegation>Execute sub-task</codex_delegation>"},
			{"type":"function_call","call_id":"call_1789387253098037589_85","name":"Bash","arguments":"{\"command\":\"pwd\"}"},
			{"type":"function_call_output","call_id":"call_1789387253098037589_85","output":"/Users/developer"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4.1-flash", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()

	delegationFound := false
	bashToolFound := false
	for _, message := range messages {
		role := message.Get("role").String()
		if role == "tool" && message.Get("tool_call_id").String() == "call_missing" {
			t.Fatalf("unpaired output emitted as tool message: %s", string(out))
		}
		if role == "user" && strings.Contains(message.Get("content").String(), "<codex_delegation>") {
			delegationFound = true
		}
		if role == "tool" && message.Get("tool_call_id").String() == "call_1789387253098037589_85" {
			bashToolFound = true
		}
	}
	if !delegationFound {
		t.Fatalf("expected unpaired send_message_to_thread output as user content; output=%s", string(out))
	}
	if !bashToolFound {
		t.Fatalf("expected paired Bash tool message; output=%s", string(out))
	}
}
