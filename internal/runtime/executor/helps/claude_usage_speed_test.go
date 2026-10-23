package helps

import "testing"

func TestClaudeSpeedPreservesSparseUsageAndExplicitZero(t *testing.T) {
	var buffer StreamUsageBuffer
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":20,"output_tokens":2,"cache_read_input_tokens":10,"cache_creation_input_tokens":5,"speed":"fast"}}}`))
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"input_tokens":0,"output_tokens":8,"output_tokens_details":{"thinking_tokens":3},"speed":"standard"}}`))
	buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","usage":{"speed":null}}`))
	got, ok := buffer.Detail()
	if !ok || got.InputTokens != 0 || got.OutputTokens != 8 || got.ReasoningTokens != 3 || got.CacheReadTokens != 10 || got.CacheCreationTokens != 5 || got.TotalTokens != 23 || got.ResponseServiceTier != "standard" {
		t.Fatalf("unexpected sparse usage: %+v, present=%v", got, ok)
	}
	if !got.TokenBreakdown.Valid() || got.TokenBreakdown.Output.NonReasoningTokens != 5 {
		t.Fatalf("thinking/cache accounting changed: %+v", got.TokenBreakdown)
	}
}

func TestParseClaudeUsageReportsSpeedAsResponseServiceTier(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"usage":{"input_tokens":10,"output_tokens":5,"speed":"fast"}}`, "fast"},
		{`{"usage":{"input_tokens":10,"output_tokens":5,"speed":"standard"}}`, "standard"},
		{`{"usage":{"input_tokens":10,"output_tokens":5}}`, ""},
		{`{"usage":{"speed":" standard "}}`, "standard"},
		{`{"usage":{"speed":null}}`, ""},
		{`{"usage":{"speed":false}}`, ""},
		{`{"usage":{"speed":123}}`, ""},
		{`{"usage":{"speed":{"tier":"fast"}}}`, ""},
	} {
		if got := ParseClaudeUsage([]byte(tc.body)).ResponseServiceTier; got != tc.want {
			t.Errorf("%s: ResponseServiceTier = %q, want %q", tc.body, got, tc.want)
		}
	}
}

// The speed can arrive on message_start while message_delta carries only the
// final output count; the merged stream usage must keep it.
func TestStreamUsageBufferObserveClaudeStream_KeepsSpeedAcrossDelta(t *testing.T) {
	for _, tc := range []struct{ start, delta, want string }{
		{`"speed":"fast"`, ``, "fast"},
		{``, `,"speed":"fast"`, "fast"},
		{``, ``, ""},
	} {
		var buffer StreamUsageBuffer
		start := `data: {"type":"message_start","message":{"id":"msg_1","model":"claude-opus-5","usage":{"input_tokens":20,"output_tokens":1`
		if tc.start != "" {
			start += "," + tc.start
		}
		buffer.ObserveClaudeStream([]byte(start + `}}}`))
		buffer.ObserveClaudeStream([]byte(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15` + tc.delta + `}}`))
		detail, ok := buffer.Detail()
		if !ok || detail.ResponseServiceTier != tc.want || detail.OutputTokens != 15 || detail.InputTokens != 20 {
			t.Errorf("start %q delta %q: got tier %q in=%d out=%d ok=%v, want tier %q in=20 out=15",
				tc.start, tc.delta, detail.ResponseServiceTier, detail.InputTokens, detail.OutputTokens, ok, tc.want)
		}
	}
}
