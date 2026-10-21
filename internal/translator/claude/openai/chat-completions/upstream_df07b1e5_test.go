package chat_completions

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestConvertOpenAIRequestToClaude_DropsEmptyAndWhitespaceTextBlocks(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-5",
		"messages": [
			{
				"role": "system",
				"content": [
					{"type": "text", "text": "System rule"},
					{"type": "text", "text": ""},
					{"type": "text", "text": "   "}
				],
				"cache_control": {"type": "ephemeral"}
			},
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Reply with exactly OK."},
					{"type": "text", "text": ""},
					{"type": "text", "text": " \n\t "}
				],
				"cache_control": {"type": "ephemeral"}
			}
		]
	}`

	result, err := ConvertOpenAIRequestToClaudeChecked("claude-sonnet-5", []byte(inputJSON), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	root := gjson.ParseBytes(result)

	system := root.Get("system").Array()
	if len(system) != 1 {
		t.Fatalf("system block count = %d, want 1. system=%s", len(system), root.Get("system").Raw)
	}
	if got := system[0].Get("text").String(); got != "System rule" {
		t.Fatalf("system.0.text = %q, want %q", got, "System rule")
	}
	if got := system[0].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("system.0.cache_control.type = %q, want ephemeral", got)
	}

	content := root.Get("messages.0.content").Array()
	if len(content) != 1 {
		t.Fatalf("messages.0.content count = %d, want 1. content=%s", len(content), root.Get("messages.0.content").Raw)
	}
	if got := content[0].Get("text").String(); got != "Reply with exactly OK." {
		t.Fatalf("messages.0.content.0.text = %q, want %q", got, "Reply with exactly OK.")
	}
	if got := content[0].Get("cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("messages.0.content.0.cache_control.type = %q, want ephemeral", got)
	}

	mixedTTL := `{
		"model": "claude-sonnet-5",
		"messages": [
			{
				"role": "system",
				"content": [
					{"type": "text", "text": "S1"},
					{"type": "text", "text": "", "cache_control": {"type": "ephemeral", "ttl": "1h"}},
					{"type": "text", "text": "S2", "cache_control": {"type": "ephemeral", "ttl": "5m"}},
					{"type": "text", "text": "S3"}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "A"},
					{"type": "text", "text": "", "cache_control": {"type": "ephemeral", "ttl": "1h"}},
					{"type": "text", "text": "B", "cache_control": {"type": "ephemeral", "ttl": "5m"}},
					{"type": "text", "text": "C"}
				]
			}
		]
	}`
	outMixed, errMixed := ConvertOpenAIRequestToClaudeChecked("claude-sonnet-5", []byte(mixedTTL), false)
	if errMixed != nil {
		t.Fatalf("unexpected error: %v", errMixed)
	}
	mixedRoot := gjson.ParseBytes(outMixed)
	if got := mixedRoot.Get("system.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("system.0.cache_control.ttl = %q, want 1h", got)
	}
	if got := mixedRoot.Get("system.1.cache_control.ttl").String(); got != "5m" {
		t.Fatalf("system.1.cache_control.ttl = %q, want 5m", got)
	}
	if mixedRoot.Get("system.2.cache_control").Exists() {
		t.Fatalf("system.2 must not receive cache_control from middle empty part")
	}
	if got := mixedRoot.Get("messages.0.content.0.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("messages.0.content.0.cache_control.ttl = %q, want 1h", got)
	}
	if got := mixedRoot.Get("messages.0.content.1.cache_control.ttl").String(); got != "5m" {
		t.Fatalf("messages.0.content.1.cache_control.ttl = %q, want 5m", got)
	}
	if mixedRoot.Get("messages.0.content.2.cache_control").Exists() {
		t.Fatalf("messages.0.content.2 must not receive cache_control from middle empty part")
	}
}
