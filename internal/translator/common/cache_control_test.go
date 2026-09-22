package common

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAttachToolMessageCacheControlTargetsOnlyToolResult(t *testing.T) {
	src := gjson.Parse(`{"content":[{"cache_control":{"type":"ephemeral","ttl":"1h"}}]}`)
	for _, tc := range []struct {
		name, message, path string
	}{
		{"tool after text", `{"content":[{"type":"text","text":"hello"},{"type":"tool_result","tool_use_id":"call_1","content":"result"}]}`, "content.1.cache_control.ttl"},
		{"only first tool result", `{"content":[{"type":"tool_result","tool_use_id":"call_1","content":"one"},{"type":"tool_result","tool_use_id":"call_2","content":"two"}]}`, "content.0.cache_control.ttl"},
		{"no tool result", `{"content":[{"type":"text","text":"hello"}]}`, ""},
		{"empty content", `{"content":[]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := AttachToolMessageCacheControl([]byte(tc.message), src)
			if tc.path == "" {
				if !bytes.Equal(out, []byte(tc.message)) {
					t.Fatalf("message without a tool result changed: %s", out)
				}
				return
			}
			if got := gjson.GetBytes(out, tc.path).String(); got != "1h" {
				t.Fatalf("%s = %q, want 1h; out=%s", tc.path, got, out)
			}
			count := 0
			for _, block := range gjson.GetBytes(out, "content").Array() {
				if block.Get("cache_control").Exists() {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("marked %d blocks, want 1; out=%s", count, out)
			}
		})
	}
}

func TestCacheControlToolValidationDoesNotChangeOtherMessages(t *testing.T) {
	src := gjson.Parse(`{"cache_control":{"type":"future-policy","ttl":"5m"}}`)
	part := AttachCacheControl([]byte(`{"type":"text","text":"hi"}`), src)
	message := AttachMessageCacheControl([]byte(`{"role":"user","content":"hi"}`), src)
	if gjson.GetBytes(part, "cache_control").Raw != src.Get("cache_control").Raw ||
		gjson.GetBytes(message, "content.0.cache_control").Raw != src.Get("cache_control").Raw {
		t.Fatalf("unrelated cache-control policy changed: part=%s message=%s", part, message)
	}
}

func TestAttachCacheControl_CopiesObject(t *testing.T) {
	src := gjson.Parse(`{"text":"hi","cache_control":{"type":"ephemeral","ttl":"5m"}}`)
	dst := []byte(`{"type":"text","text":"hi"}`)

	out := AttachCacheControl(dst, src)
	if got := gjson.GetBytes(out, "cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("cache_control.type = %q, want ephemeral; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "cache_control.ttl").String(); got != "5m" {
		t.Fatalf("cache_control.ttl = %q, want 5m; out=%s", got, out)
	}
}

func TestAttachCacheControl_IgnoresMissing(t *testing.T) {
	src := gjson.Parse(`{"text":"hi"}`)
	dst := []byte(`{"type":"text","text":"hi"}`)

	out := AttachCacheControl(dst, src)
	if gjson.GetBytes(out, "cache_control").Exists() {
		t.Fatalf("cache_control should be absent; out=%s", out)
	}
}

func TestAttachMessageCacheControl_PromotesStringContent(t *testing.T) {
	src := gjson.Parse(`{"role":"user","content":"hi","cache_control":{"type":"ephemeral"}}`)
	msg := []byte(`{"role":"user","content":"hi"}`)

	out := AttachMessageCacheControl(msg, src)
	if got := gjson.GetBytes(out, "content.0.type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.0.text").String(); got != "hi" {
		t.Fatalf("content.0.text = %q, want hi; out=%s", got, out)
	}
	if got := gjson.GetBytes(out, "content.0.cache_control.type").String(); got != "ephemeral" {
		t.Fatalf("content.0.cache_control.type = %q, want ephemeral; out=%s", got, out)
	}
}

func TestAttachMessageCacheControl_SkipsWhenLastPartHasCacheControl(t *testing.T) {
	src := gjson.Parse(`{"cache_control":{"type":"ephemeral","ttl":"1h"}}`)
	msg := []byte(`{"role":"user","content":[{"type":"text","text":"hi","cache_control":{"type":"ephemeral"}}]}`)

	out := AttachMessageCacheControl(msg, src)
	if gjson.GetBytes(out, "content.0.cache_control.ttl").Exists() {
		t.Fatalf("part-level cache_control should win; out=%s", out)
	}
}
