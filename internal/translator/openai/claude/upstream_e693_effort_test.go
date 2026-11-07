package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToOpenAI_StandaloneOutputConfigEffort(t *testing.T) {
	tests := []struct {
		name       string
		inputJSON  string
		wantEffort string
	}{
		{
			name:       "standalone high effort without thinking",
			inputJSON:  `{"model":"gpt-5.4","max_tokens":16,"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`,
			wantEffort: "high",
		},
		{
			name:       "standalone effort is trimmed and lowercased",
			inputJSON:  `{"output_config":{"effort":" HIGH "}}`,
			wantEffort: "high",
		},
		{
			name:       "empty effort without thinking omits reasoning_effort",
			inputJSON:  `{"output_config":{"effort":""}}`,
			wantEffort: "",
		},
		{
			name:       "whitespace-only effort without thinking omits reasoning_effort",
			inputJSON:  `{"output_config":{"effort":"   "}}`,
			wantEffort: "",
		},
		{
			name:       "non-string effort without thinking omits reasoning_effort",
			inputJSON:  `{"output_config":{"effort":123}}`,
			wantEffort: "",
		},
		{
			name:       "empty thinking object does not use standalone effort",
			inputJSON:  `{"thinking":{},"output_config":{"effort":"high"}}`,
			wantEffort: "",
		},
		{
			name:       "unknown thinking type does not use standalone effort",
			inputJSON:  `{"thinking":{"type":"unknown"},"output_config":{"effort":"high"}}`,
			wantEffort: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, _ := ConvertClaudeRequestToOpenAIChecked("test-model", []byte(tt.inputJSON), false)
			effort := gjson.GetBytes(out, "reasoning_effort")
			if effort.String() != tt.wantEffort || (tt.wantEffort == "" && effort.Exists()) {
				t.Fatalf("reasoning_effort = %s, want %q; body=%s", effort.Raw, tt.wantEffort, out)
			}
		})
	}
}
