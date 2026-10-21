package responses

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestConvertOpenAIResponsesRequestToClaude_DropsEmptyAndWhitespaceTextBlocks(t *testing.T) {
	t.Run("trailing empty and whitespace input_text dropped", func(t *testing.T) {
		raw := []byte(`{
			"model": "claude-sonnet-5",
			"instructions": "   ",
			"input": [
				{
					"role": "developer",
					"content": [
						{"type": "input_text", "text": "Follow instructions."},
						{"type": "input_text", "text": ""},
						{"type": "input_text", "text": "  \n\t "}
					],
					"cache_control": {"type": "ephemeral"}
				},
				{
					"role": "user",
					"content": [
						{"type": "input_text", "text": "Reply with exactly OK."},
						{"type": "input_text", "text": ""},
						{"type": "input_text", "text": "   "}
					]
				},
				{
					"role": "assistant",
					"content": [
						{"type": "output_text", "text": ""},
						{"type": "output_text", "text": "OK"},
						{"type": "output_text", "text": " \t "}
					]
				},
				{
					"role": "user",
					"content": [
						{"type": "input_text", "text": "Next turn"},
						{"type": "input_text", "text": "", "cache_control": {"type": "ephemeral"}}
					]
				}
			],
			"reasoning": {"effort": "minimal"},
			"max_output_tokens": 128,
			"store": false
		}`)

		out, err := ConvertOpenAIResponsesRequestToClaudeChecked("claude-sonnet-5", raw, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		root := gjson.ParseBytes(out)

		system := root.Get("system").Array()
		if len(system) != 1 {
			t.Fatalf("system block count = %d, want 1. system=%s", len(system), root.Get("system").Raw)
		}
		if got := system[0].Get("text").String(); got != "Follow instructions." {
			t.Fatalf("system.0.text = %q, want %q", got, "Follow instructions.")
		}
		if got := system[0].Get("cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("system.0.cache_control.type = %q, want ephemeral", got)
		}

		messages := root.Get("messages").Array()
		if len(messages) != 3 {
			t.Fatalf("messages count = %d, want 3. messages=%s", len(messages), root.Get("messages").Raw)
		}
		if got := messages[0].Get("content").String(); got != "Reply with exactly OK." {
			t.Fatalf("messages.0.content = %q, want %q (raw=%s)", got, "Reply with exactly OK.", messages[0].Get("content").Raw)
		}
		if got := messages[1].Get("content").String(); got != "OK" {
			t.Fatalf("messages.1.content = %q, want %q (raw=%s)", got, "OK", messages[1].Get("content").Raw)
		}
		if got := messages[2].Get("content.0.text").String(); got != "Next turn" {
			t.Fatalf("messages.2.content.0.text = %q, want %q (raw=%s)", got, "Next turn", messages[2].Get("content").Raw)
		}
		if got := messages[2].Get("content.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("messages.2.content.0.cache_control.type = %q, want ephemeral (raw=%s)", got, messages[2].Get("content").Raw)
		}
	})

	t.Run("middle empty part with cache_control attaches to immediately preceding block preserving TTL order", func(t *testing.T) {
		raw := []byte(`{
			"model": "claude-sonnet-5",
			"input": [
				{
					"role": "developer",
					"content": [
						{"type": "input_text", "text": "S1"},
						{"type": "input_text", "text": "", "cache_control": {"type": "ephemeral", "ttl": "1h"}},
						{"type": "input_text", "text": "S2", "cache_control": {"type": "ephemeral", "ttl": "5m"}},
						{"type": "input_text", "text": "S3"}
					]
				},
				{
					"role": "user",
					"content": [
						{"type": "input_text", "text": "A"},
						{"type": "input_text", "text": "", "cache_control": {"type": "ephemeral", "ttl": "1h"}},
						{"type": "input_text", "text": "B", "cache_control": {"type": "ephemeral", "ttl": "5m"}},
						{"type": "input_text", "text": "C"}
					]
				},
				{
					"role": "user",
					"content": [
						{"type": "input_text", "text": ""},
						{"type": "input_text", "text": "   "}
					]
				}
			]
		}`)
		out, err := ConvertOpenAIResponsesRequestToClaudeChecked("claude-sonnet-5", raw, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		root := gjson.ParseBytes(out)

		if got := root.Get("system.#").Int(); got != 3 {
			t.Fatalf("system count = %d, want 3. system=%s", got, root.Get("system").Raw)
		}
		if got := root.Get("system.0.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("system.0.cache_control.ttl = %q, want 1h. system=%s", got, root.Get("system").Raw)
		}
		if got := root.Get("system.1.cache_control.ttl").String(); got != "5m" {
			t.Fatalf("system.1.cache_control.ttl = %q, want 5m. system=%s", got, root.Get("system").Raw)
		}
		if root.Get("system.2.cache_control").Exists() {
			t.Fatalf("system.2 must not receive cache_control from middle empty part. system=%s", root.Get("system").Raw)
		}

		if got := root.Get("messages.#").Int(); got != 1 {
			t.Fatalf("messages count = %d, want 1. out=%s", got, out)
		}
		if got := root.Get("messages.0.content.#").Int(); got != 3 {
			t.Fatalf("messages.0.content count = %d, want 3. content=%s", got, root.Get("messages.0.content").Raw)
		}
		if got := root.Get("messages.0.content.0.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("messages.0.content.0.cache_control.ttl = %q, want 1h. content=%s", got, root.Get("messages.0.content").Raw)
		}
		if got := root.Get("messages.0.content.1.cache_control.ttl").String(); got != "5m" {
			t.Fatalf("messages.0.content.1.cache_control.ttl = %q, want 5m. content=%s", got, root.Get("messages.0.content").Raw)
		}
		if root.Get("messages.0.content.2.cache_control").Exists() {
			t.Fatalf("messages.0.content.2 must not receive cache_control from middle empty part. content=%s", root.Get("messages.0.content").Raw)
		}
	})
}
