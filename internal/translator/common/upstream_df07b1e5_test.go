package common

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestAttachMessageCacheControl_SkipsEmptyAndWhitespaceTextBlocks(t *testing.T) {
	src := gjson.Parse(`{"cache_control":{"type":"ephemeral"}}`)

	t.Run("picks last non-empty block when trailing blocks are empty", func(t *testing.T) {
		msg := []byte(`{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":""},{"type":"text","text":"   "}]}`)
		out := AttachMessageCacheControl(msg, src)
		if got := gjson.GetBytes(out, "content.0.cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("content.0.cache_control.type = %q, want ephemeral; out=%s", got, out)
		}
		if gjson.GetBytes(out, "content.1.cache_control").Exists() || gjson.GetBytes(out, "content.2.cache_control").Exists() {
			t.Fatalf("empty/whitespace text blocks must not receive cache_control; out=%s", out)
		}
	})

	t.Run("skips when all blocks or string are empty", func(t *testing.T) {
		for _, msg := range [][]byte{
			[]byte(`{"role":"user","content":""}`),
			[]byte(`{"role":"user","content":"   "}`),
			[]byte(`{"role":"user","content":[{"type":"text","text":""},{"type":"text","text":" \n "}]}`),
		} {
			out := AttachMessageCacheControl(msg, src)
			if gjson.GetBytes(out, "content.0.cache_control").Exists() {
				t.Fatalf("empty message content must not receive cache_control; out=%s", out)
			}
		}
	})
}
