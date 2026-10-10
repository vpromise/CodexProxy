package responses

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesClaudeDefaultOutputLimit(t *testing.T) {
	cases := []struct {
		name, model, field string
		want               int64
	}{
		{"opus omitted", "claude-opus-5-5", "", 128000},
		{"opus null", "claude-opus-5-5", `,"max_output_tokens":null`, 128000},
		{"sonnet omitted", "claude-sonnet-4-6", "", 64000},
		{"haiku smaller limit", "claude-3-5-haiku-20241022", "", 8192},
		{"unknown fallback", "unknown-claude-model", "", 32000},
		{"fable retains local ceiling", "claude-fable-5-1", "", 32000},
		{"explicit caller limit", "claude-opus-5-5", `,"max_output_tokens":256`, 256},
		{"explicit zero is preserved", "claude-opus-5-5", `,"max_output_tokens":0`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(fmt.Sprintf(`{"model":%q,"input":"hello"%s}`, tc.model, tc.field))
			for _, stream := range []bool{false, true} {
				out := ConvertOpenAIResponsesRequestToClaude(tc.model, input, stream)
				if got := gjson.GetBytes(out, "max_tokens").Int(); got != tc.want {
					t.Errorf("stream=%t max_tokens=%d, want %d", stream, got, tc.want)
				}
			}
		})
	}
}
