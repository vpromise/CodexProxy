package helps

import (
	"testing"
)

func TestSyncOctober21ParseOpenAIUsageFlatReasoningDialect(t *testing.T) {
	// SGLang-style usage: reasoning_tokens flat on the usage object.
	data := []byte(`{"usage":{"prompt_tokens":241114,"total_tokens":242270,"completion_tokens":1156,"prompt_tokens_details":{"cached_tokens":240896},"reasoning_tokens":530}}`)
	detail := ParseOpenAIUsage(data)

	if detail.ReasoningTokens != 530 {
		t.Fatalf("reasoning tokens = %d, want 530", detail.ReasoningTokens)
	}
	if detail.InputTokens != 241114 || detail.OutputTokens != 1156 || detail.TotalTokens != 242270 {
		t.Fatalf("token counts = %+v", detail)
	}
	if detail.CachedTokens != 240896 || detail.CacheReadTokens != 240896 {
		t.Fatalf("cached tokens = %d/%d", detail.CachedTokens, detail.CacheReadTokens)
	}
	if !detail.TokenBreakdown.Valid() {
		t.Fatalf("token breakdown = %+v", detail.TokenBreakdown)
	}

	// Streaming variant must behave the same.
	streamDetail, ok := ParseOpenAIStreamUsage([]byte(`data: {"id":"x","object":"chat.completion.chunk","usage":{"prompt_tokens":10,"completion_tokens":6,"total_tokens":16,"reasoning_tokens":4}}`))
	if !ok {
		t.Fatalf("ParseOpenAIStreamUsage reported no usage")
	}
	if streamDetail.ReasoningTokens != 4 {
		t.Fatalf("stream reasoning tokens = %d, want 4", streamDetail.ReasoningTokens)
	}
}

func TestSyncOctober21FlatReasoningDoesNotOverrideNestedZero(t *testing.T) {
	for _, raw := range []string{
		`{"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":0},"reasoning_tokens":4}}`,
		`{"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8,"output_tokens_details":{"reasoning_tokens":0},"reasoning_tokens":4}}`,
	} {
		got := ParseOpenAIUsage([]byte(raw))
		if got.ReasoningTokens != 0 || got.TotalTokens != 8 || !got.TokenBreakdown.Valid() {
			t.Fatalf("unexpected accounting: %+v", got)
		}
	}
	got := ParseOpenAIUsage([]byte(`{"usage":{"reasoning_tokens":4}}`))
	if got.ReasoningTokens != 4 {
		t.Fatalf("flat-only reasoning not recognized: %+v", got)
	}
}
