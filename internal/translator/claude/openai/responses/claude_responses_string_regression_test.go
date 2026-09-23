package responses

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestConvertOpenAIResponsesRequestToClaude_StringInput(t *testing.T) {
	inputJSON := []byte(`{
		"model": "claude-sonnet-4-6",
		"input": "hi",
		"max_output_tokens": 16,
		"stream": false
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-6", inputJSON, false)
	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 1 {
		t.Fatalf("expected 1 message in translated Claude request, got %d. Output: %s", len(messages), string(out))
	}

	msg := messages[0]
	if got := msg.Get("role").String(); got != "user" {
		t.Fatalf("expected message role %q, got %q", "user", got)
	}

	content := msg.Get("content")
	var text string
	if content.IsArray() {
		parts := content.Array()
		if len(parts) != 1 {
			t.Fatalf("expected 1 content part, got %d", len(parts))
		}
		if got := parts[0].Get("type").String(); got != "text" {
			t.Fatalf("expected block type text, got %s", got)
		}
		text = parts[0].Get("text").String()
	} else {
		text = content.String()
	}

	if text != "hi" {
		t.Fatalf("expected user message content %q, got %q", "hi", text)
	}

	// Also verify string input with instructions produces both system and user message.
	withInstructions := []byte(`{
		"model": "claude-sonnet-4-6",
		"instructions": "Be concise.",
		"input": "hello world",
		"max_output_tokens": 32,
		"stream": false
	}`)
	outWithInstr := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-6", withInstructions, false)
	system := gjson.GetBytes(outWithInstr, "system").Array()
	if len(system) != 1 || system[0].Get("text").String() != "Be concise." {
		t.Fatalf("expected system block with instructions, got %s", string(outWithInstr))
	}
	messagesWithInstr := gjson.GetBytes(outWithInstr, "messages").Array()
	if len(messagesWithInstr) != 1 {
		t.Fatalf("expected 1 message in translated Claude request with instructions, got %d. Output: %s", len(messagesWithInstr), string(outWithInstr))
	}
	if messagesWithInstr[0].Get("role").String() != "user" || messagesWithInstr[0].Get("content").String() != "hello world" {
		t.Fatalf("unexpected user message in output: %s", string(outWithInstr))
	}

	// Also verify string input with quotes, newlines, and unicode.
	complexInput := []byte(`{
		"model": "claude-sonnet-4-6",
		"input": "line 1\n\"line 2\"\n你好，世界 🌍",
		"max_output_tokens": 16,
		"stream": false
	}`)
	outComplex := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-6", complexInput, false)
	complexMsgs := gjson.GetBytes(outComplex, "messages").Array()
	if len(complexMsgs) != 1 {
		t.Fatalf("expected 1 message in translated Claude request with complex input, got %d. Output: %s", len(complexMsgs), string(outComplex))
	}
	if got := complexMsgs[0].Get("content").String(); got != "line 1\n\"line 2\"\n你好，世界 🌍" {
		t.Fatalf("unexpected content for complex input, got %q", got)
	}
}
