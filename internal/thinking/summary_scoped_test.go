package thinking

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractSummaryConfigForSupportedFormats(t *testing.T) {
	tests := []struct {
		name       string
		format     string
		body       string
		wantMode   SummaryMode
		wantDetail string
	}{
		{name: "OpenAI enabled", format: "openai", body: `{"reasoning_effort":"high"}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "OpenAI disabled", format: "openai", body: `{"reasoning_effort":"none"}`, wantMode: SummaryDisabled},
		{name: "Responses enabled", format: "openai-response", body: `{"reasoning":{"summary":"detailed"}}`, wantMode: SummaryEnabled, wantDetail: "detailed"},
		{name: "Responses disabled", format: "openai-response", body: `{"reasoning":{"summary":null}}`, wantMode: SummaryDisabled},
		{name: "Claude enabled", format: "claude", body: `{"thinking":{"type":"adaptive","display":"summarized"}}`, wantMode: SummaryEnabled, wantDetail: "auto"},
		{name: "unsupported format", format: "unsupported", body: `{"reasoning":{"summary":"auto"}}`, wantMode: SummaryUnspecified},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := ExtractSummaryConfig([]byte(testCase.body), testCase.format)
			if got.Mode != testCase.wantMode || got.Detail != testCase.wantDetail {
				t.Fatalf("ExtractSummaryConfig() = %+v, want mode=%v detail=%q", got, testCase.wantMode, testCase.wantDetail)
			}
		})
	}
}

func TestApplySummaryConfigForSupportedFormats(t *testing.T) {
	responses := ApplySummaryConfig([]byte(`{"reasoning":{"effort":"high"}}`), "openai-response", SummaryConfig{Mode: SummaryEnabled, Detail: "concise"})
	if got := gjson.GetBytes(responses, "reasoning.summary").String(); got != "concise" {
		t.Fatalf("Responses reasoning.summary = %q, want concise", got)
	}

	codex := ApplySummaryConfig([]byte(`{"reasoning":{"effort":"high","summary":"auto"}}`), "codex", SummaryConfig{Mode: SummaryDisabled})
	if gjson.GetBytes(codex, "reasoning.summary").Exists() {
		t.Fatalf("Codex reasoning.summary was not removed: %s", codex)
	}

	claude := ApplySummaryConfig([]byte(`{"thinking":{"type":"adaptive"}}`), "claude", SummaryConfig{Mode: SummaryEnabled})
	if got := gjson.GetBytes(claude, "thinking.display").String(); got != "summarized" {
		t.Fatalf("Claude thinking.display = %q, want summarized", got)
	}
}

func TestExtractReasoningEffortForSupportedFormats(t *testing.T) {
	if got := ExtractReasoningEffort([]byte(`{"reasoning":{"effort":"high"}}`), "codex", "gpt-5.4"); got != "high" {
		t.Fatalf("Codex effort = %q, want high", got)
	}
	if got := ExtractReasoningEffort([]byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`), "claude", "claude-opus-5"); got != "max" {
		t.Fatalf("Claude effort = %q, want max", got)
	}
}
