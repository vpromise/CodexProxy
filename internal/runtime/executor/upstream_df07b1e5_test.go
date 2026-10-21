package executor

import (
	"github.com/tidwall/gjson"
	"testing"
	"time"
)

func TestEnsureCacheControlSkipsEmptyAndWhitespaceTextBlocks(t *testing.T) {
	t.Run("Message With Trailing Empty And Whitespace Text Blocks", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-sonnet-5",
			"messages": [{
				"role": "user",
				"content": [
					{"type": "text", "text": "Reply with exactly OK."},
					{"type": "text", "text": ""},
					{"type": "text", "text": "  \n\t "}
				]
			}]
		}`)
		output := ensureCacheControl(input)
		if got := gjson.GetBytes(output, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("messages.0.content.0.cache_control.type = %q, want ephemeral. Output: %s", got, output)
		}
		if gjson.GetBytes(output, "messages.0.content.1.cache_control").Exists() || gjson.GetBytes(output, "messages.0.content.2.cache_control").Exists() {
			t.Fatalf("empty/whitespace text block must not receive cache_control: %s", output)
		}
	})

	t.Run("Trailing Message With Only Empty Text Falls Back To Earlier Message", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-sonnet-5",
			"messages": [
				{"role": "user", "content": [{"type": "text", "text": "Real question"}]},
				{"role": "assistant", "content": [{"type": "text", "text": ""}]},
				{"role": "user", "content": "   "}
			]
		}`)
		output := ensureCacheControl(input)
		if got := gjson.GetBytes(output, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("messages.0.content.0.cache_control.type = %q, want fallback ephemeral. Output: %s", got, output)
		}
		if gjson.GetBytes(output, "messages.1.content.0.cache_control").Exists() || gjson.GetBytes(output, "messages.2.content.0.cache_control").Exists() {
			t.Fatalf("empty messages must not receive cache_control: %s", output)
		}
	})

	t.Run("System-Only Fallback Empty User Message Is Never Stamped", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-sonnet-5",
			"system": [{"type": "text", "text": "You are a helpful assistant."}],
			"messages": [{"role": "user", "content": [{"type": "text", "text": ""}]}]
		}`)
		output := ensureCacheControl(input)
		if got := gjson.GetBytes(output, "system.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("system.0.cache_control.type = %q, want ephemeral. Output: %s", got, output)
		}
		if gjson.GetBytes(output, "messages.0.content.0.cache_control").Exists() {
			t.Fatalf("fallback empty user text block must not receive cache_control: %s", output)
		}
	})

	t.Run("System Array With Trailing Empty Text Stamps Last Non-Empty Element", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-sonnet-5",
			"system": [
				{"type": "text", "text": "Valid system prompt"},
				{"type": "text", "text": ""},
				{"type": "text", "text": "   "}
			],
			"messages": [{"role": "user", "content": "Hi"}]
		}`)
		output := ensureCacheControl(input)
		if got := gjson.GetBytes(output, "system.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("system.0.cache_control.type = %q, want ephemeral. Output: %s", got, output)
		}
		if gjson.GetBytes(output, "system.1.cache_control").Exists() || gjson.GetBytes(output, "system.2.cache_control").Exists() {
			t.Fatalf("empty system blocks must not receive cache_control: %s", output)
		}
	})

	t.Run("System Array With Only Empty Text Falls Back To Tools", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-sonnet-5",
			"system": [{"type": "text", "text": ""}, {"type": "text", "text": "   "}],
			"tools": [{"name": "tool1", "description": "Tool", "input_schema": {"type": "object"}}],
			"messages": [{"role": "user", "content": "Hi"}]
		}`)
		output := ensureCacheControl(input)
		if got := gjson.GetBytes(output, "tools.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("tools.0.cache_control.type = %q, want ephemeral fallback when system array has only empty blocks: %s", got, output)
		}
		if gjson.GetBytes(output, "system.0.cache_control").Exists() || gjson.GetBytes(output, "system.1.cache_control").Exists() {
			t.Fatalf("empty system array elements must not receive cache_control: %s", output)
		}
	})

	t.Run("Cloaking CurrentDate Injection Skips Empty Text Blocks", func(t *testing.T) {
		input := []byte(`{
			"model": "claude-sonnet-5",
			"messages": [{
				"role": "user",
				"content": [
					{"type": "text", "text": ""},
					{"type": "text", "text": "Actual user prompt"}
				]
			}]
		}`)
		output := injectClaudeCodeCurrentDate(input, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC))
		if gjson.GetBytes(output, "messages.0.content.1.cache_control").Exists() {
			t.Fatalf("empty text block at index 1 must not receive cloaking cache_control: %s", output)
		}
		if got := gjson.GetBytes(output, "messages.0.content.2.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("first non-empty user text block at index 2 should receive cloaking cache_control, got %q: %s", got, output)
		}
	})
}
