package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeCodexReasoningContent(t *testing.T) {
	for _, tc := range []struct {
		name        string
		item        string
		wantSummary string
	}{
		{"empty summary", `{"type":"reasoning","summary":[],"content":[{"type":"reasoning_text","text":"old provider thought"}]}`, `[{"type":"summary_text","text":"old provider thought"}]`},
		{"missing summary", `{"type":"reasoning","content":[{"type":"reasoning_text","text":"first"},{"type":"reasoning_text","text":"second"}]}`, `[{"type":"summary_text","text":"first"},{"type":"summary_text","text":"second"}]`},
		{"null summary", `{"type":"reasoning","summary":null,"content":[{"type":"reasoning_text","text":"thought"}]}`, `[{"type":"summary_text","text":"thought"}]`},
		{"preserve distinct text", `{"type":"reasoning","summary":[{"type":"summary_text","text":"short summary"}],"content":[{"type":"reasoning_text","text":"additional detail"}]}`, `[{"type":"summary_text","text":"short summary"},{"type":"summary_text","text":"additional detail"}]`},
		{"already summarized", `{"type":"reasoning","summary":[{"type":"summary_text","text":"same"}],"content":[{"type":"reasoning_text","text":"same"}]}`, `[{"type":"summary_text","text":"same"}]`},
		{"empty text", `{"type":"reasoning","content":[{"type":"reasoning_text","text":""}]}`, `[]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"input":[` + tc.item + `,{"type":"message","role":"user","content":"next"}],"metadata":{"future":true}}`)
			before := bytes.Clone(body)
			got := NormalizeCodexReasoningContent(body)
			if gjson.GetBytes(got, "input.0.content").Raw != "[]" || gjson.GetBytes(got, "input.0.summary").Raw != tc.wantSummary {
				t.Fatalf("normalized reasoning = %s", got)
			}
			if !bytes.Equal(body, before) || gjson.GetBytes(got, "input.1").Raw != gjson.GetBytes(body, "input.1").Raw || !gjson.GetBytes(got, "metadata.future").Bool() {
				t.Fatalf("normalization changed unrelated content: %s", got)
			}
			if again := NormalizeCodexReasoningContent(got); !bytes.Equal(again, got) {
				t.Fatalf("normalization is not idempotent: %s", again)
			}
		})
	}
}

func TestNormalizeCodexReasoningContentPreservesOtherShapes(t *testing.T) {
	for _, body := range []string{
		`{"input":"hello"}`,
		`{"input":[{"type":"reasoning","summary":[],"content":[]}]}`,
		`{"input":[{"type":"reasoning","summary":[],"content":[{"type":"future_part","data":"keep"}]}]}`,
		`{"input":[{"type":"reasoning","summary":"unknown","content":[{"type":"reasoning_text","text":"keep"}]}]}`,
		`{"input":[{"type":"message","role":"assistant","content":[{"type":"reasoning_text","text":"keep"}]}]}`,
	} {
		if got := NormalizeCodexReasoningContent([]byte(body)); string(got) != body {
			t.Fatalf("unexpected rewrite: %s", got)
		}
	}
}

func TestNormalizeCodexReasoningContentPreservesEncryptedState(t *testing.T) {
	body := []byte(`{"input":[{"id":"rs_1","type":"reasoning","encrypted_content":"opaque-state","summary":[],"content":[{"type":"reasoning_text","text":"thought"}],"future":{"keep":true}}]}`)
	got := NormalizeCodexReasoningContent(body)
	if gjson.GetBytes(got, "input.0.id").String() != "rs_1" || gjson.GetBytes(got, "input.0.encrypted_content").String() != "opaque-state" || !gjson.GetBytes(got, "input.0.future.keep").Bool() {
		t.Fatalf("encrypted reasoning state changed: %s", got)
	}
}
