package executor

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeExecutor_NullCacheControlIgnored(t *testing.T) {
	// 1 real cache_control followed by 5 "cache_control": null blocks.
	payload := []byte(`{
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "block 0", "cache_control": {"type": "ephemeral"}},
					{"type": "text", "text": "block 1", "cache_control": null},
					{"type": "text", "text": "block 2", "cache_control": null},
					{"type": "text", "text": "block 3", "cache_control": null},
					{"type": "text", "text": "block 4", "cache_control": null},
					{"type": "text", "text": "block 5", "cache_control": null}
				]
			}
		]
	}`)

	// countCacheControls must count only valid breakpoints, ignoring null.
	if got := countCacheControls(payload); got != 1 {
		t.Fatalf("countCacheControls = %d, want 1 (null cache_control must not be counted)", got)
	}

	// enforceCacheControlLimit must not strip the real cache breakpoint when nulls are present.
	out := enforceCacheControlLimit(payload, 4)
	if got := countCacheControls(out); got != 1 {
		t.Fatalf("countCacheControls after enforce = %d, want 1", got)
	}
	if !gjson.GetBytes(out, "messages.0.content.0.cache_control").Exists() {
		t.Fatalf("real cache_control at messages.0.content.0 was incorrectly stripped")
	}
	if gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String() != "ephemeral" {
		t.Fatalf("messages.0.content.0.cache_control.type = %q, want ephemeral", gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String())
	}

	// Auto-injection must not be suppressed by "cache_control": null in system or tools.
	toolsPayload := []byte(`{
		"tools": [{"name": "tool1", "description": "desc", "cache_control": null}]
	}`)
	injectedTools := injectToolsCacheControl(toolsPayload)
	if got := gjson.GetBytes(injectedTools, "tools.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("injectToolsCacheControl should inject cache_control when existing value is null, got %q", got)
	}

	systemPayload := []byte(`{
		"system": [{"type": "text", "text": "sys", "cache_control": null}]
	}`)
	injectedSystem := injectSystemCacheControl(systemPayload)
	if got := gjson.GetBytes(injectedSystem, "system.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("injectSystemCacheControl should inject cache_control when existing value is null, got %q", got)
	}

	messagesPayload := []byte(`{
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello", "cache_control": null}]}
		]
	}`)
	injectedMessages := injectMessagesCacheControl(messagesPayload)
	if got := gjson.GetBytes(injectedMessages, "messages.0.content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("injectMessagesCacheControl should inject cache_control when existing value is null, got %q", got)
	}

	// enforceCacheControlLimit stripping loop when total real marks > 4 and nulls are present.
	excessWithNullPayload := []byte(`{
		"tools": [
			{"name": "t1", "cache_control": {"type": "ephemeral"}},
			{"name": "t2", "cache_control": null},
			{"name": "t3", "cache_control": {"type": "ephemeral"}}
		],
		"system": [
			{"type": "text", "text": "s1", "cache_control": {"type": "ephemeral"}},
			{"type": "text", "text": "s2", "cache_control": null}
		],
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "u1", "cache_control": {"type": "ephemeral"}},
					{"type": "text", "text": "u2", "cache_control": null},
					{"type": "text", "text": "u3", "cache_control": {"type": "ephemeral"}}
				]
			}
		]
	}`)
	// Total real marks = 2 tools + 1 system + 2 messages = 5 real marks (and 3 nulls).
	// Limit 4 means 1 real mark should be stripped (specifically t1, non-last tool).
	outExcess := enforceCacheControlLimit(excessWithNullPayload, 4)
	if got := countCacheControls(outExcess); got != 4 {
		t.Fatalf("countCacheControls after enforce with excess = %d, want 4", got)
	}
	if gjson.GetBytes(outExcess, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be removed first (non-last tool)")
	}
	if !gjson.GetBytes(outExcess, "tools.2.cache_control").Exists() {
		t.Fatalf("last tool cache_control should be preserved")
	}

	// normalizeCacheControlTTL must not treat "cache_control": null as a 5m breakpoint that downgrades 1h TTL.
	mixedTTLPayload := []byte(`{
		"system": [
			{"type": "text", "text": "sys0", "cache_control": null},
			{"type": "text", "text": "sys1", "cache_control": {"type": "ephemeral", "ttl": "1h"}}
		]
	}`)
	normalized := normalizeCacheControlTTL(mixedTTLPayload)
	if got := gjson.GetBytes(normalized, "system.1.cache_control.ttl").String(); got != "1h" {
		t.Fatalf("normalizeCacheControlTTL downgraded 1h TTL due to null cache_control: %s", normalized)
	}
}

func TestClaudeCacheControlAbsentValues(t *testing.T) {
	for _, raw := range []string{"null", "false", "1", `"legacy"`, "[]"} {
		t.Run(raw, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"tools":[{"name":"tool","cache_control":%s}],"system":[{"type":"text","text":"prefix","cache_control":%s},{"type":"text","text":"cached","cache_control":{"type":"ephemeral","ttl":"1h"}}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":%s}]}]}`, raw, raw, raw))
			if got := countCacheControls(body); got != 1 {
				t.Errorf("count = %d, want 1", got)
			}
			if out := enforceCacheControlLimit(body, 4); string(out) != string(body) {
				t.Errorf("under-limit body changed: %s", out)
			}
			if out := normalizeCacheControlTTL(body); gjson.GetBytes(out, "system.1.cache_control.ttl").String() != "1h" {
				t.Errorf("absent mark downgraded TTL: %s", out)
			}
			if out := injectToolsCacheControl(body); gjson.GetBytes(out, "tools.0.cache_control.type").String() != "ephemeral" {
				t.Errorf("tool mark not injected: %s", out)
			}
			if out := injectMessagesCacheControl(body); gjson.GetBytes(out, "messages.0.content.0.cache_control.type").String() != "ephemeral" {
				t.Errorf("message mark not injected: %s", out)
			}
			sys := []byte(fmt.Sprintf(`{"system":[{"type":"text","text":"sys","cache_control":%s}]}`, raw))
			if out := injectSystemCacheControl(sys); gjson.GetBytes(out, "system.0.cache_control.type").String() != "ephemeral" {
				t.Errorf("system mark not injected: %s", out)
			}
		})
	}
}
func TestClaudeCacheControlObjectsPreserved(t *testing.T) {
	for _, raw := range []string{`{}`, `{"type":"custom","ttl":"1h","extra":true}`} {
		body := []byte(fmt.Sprintf(`{"tools":[{"name":"tool","cache_control":%s}],"system":[{"type":"text","text":"sys","cache_control":%s}],"messages":[{"role":"user","content":[{"type":"text","text":"hi","cache_control":%s}]}]}`, raw, raw, raw))
		if countCacheControls(body) != 3 {
			t.Fatal("existing object acceptance changed")
		}
		for _, fn := range []func([]byte) []byte{injectToolsCacheControl, injectSystemCacheControl, injectMessagesCacheControl, normalizeCacheControlTTL} {
			if out := fn(body); string(out) != string(body) {
				t.Fatalf("existing object changed: %s", out)
			}
		}
	}
}
