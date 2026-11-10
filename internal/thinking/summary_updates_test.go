package thinking

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestClaudeSummaryUpdatesCanonicalRoundTrip(t *testing.T) {
	for _, display := range []string{"updates", " UpDaTeS "} {
		body := []byte(`{"thinking":{"type":"adaptive","display":"` + display + `"},"messages":[{"role":"user","content":"hello"}]}`)
		cfg := ExtractSummaryConfig(body, "claude")
		if cfg.Mode != SummaryEnabled || cfg.Detail != "auto" {
			t.Fatalf("updates not enabled: %+v", cfg)
		}
		out := ApplySummaryConfig(body, "claude", cfg)
		if got := gjson.GetBytes(out, "thinking.display").String(); got != "updates" {
			t.Fatalf("display=%q", got)
		}
		responses := ApplySummaryConfig([]byte(`{"input":[],"reasoning":{"effort":"high"}}`), "openai-response", cfg)
		if gjson.GetBytes(responses, "reasoning.summary").String() != "auto" {
			t.Fatalf("Responses summary lost: %s", responses)
		}
		disabled := ApplySummaryConfig(out, "claude", SummaryConfig{Mode: SummaryDisabled})
		if gjson.GetBytes(disabled, "thinking.display").String() != "omitted" {
			t.Fatalf("disable ignored: %s", disabled)
		}
	}
	for _, body := range []string{`{"thinking":{"display":"updates"}}`, `{"thinking":{"type":"disabled","display":"updates"}}`, `{"thinking":{"type":"enabled","budget_tokens":0,"display":"updates"}}`} {
		if c := ExtractSummaryConfig([]byte(body), "claude"); c.Mode != SummaryUnspecified {
			t.Fatalf("inactive updates enabled: %s: %+v", body, c)
		}
	}
}
