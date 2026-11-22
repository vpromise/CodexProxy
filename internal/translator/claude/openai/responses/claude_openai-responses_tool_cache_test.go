package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesPairedToolResultCacheControl(t *testing.T) {
	for _, tc := range []struct {
		name, toolCall, output, wantTTL string
		wantNested                      bool
	}{
		{
			name:     "item marker on string output",
			toolCall: `{"type":"function_call","call_id":"fixture","name":"lookup","arguments":"{}"}`,
			output:   `{"type":"function_call_output","call_id":"fixture","output":"result","cache_control":{"type":"ephemeral","ttl":"1h"}}`,
			wantTTL:  "1h",
		},
		{
			name:     "last valid part marker takes precedence",
			toolCall: `{"type":"custom_tool_call","call_id":"fixture","name":"apply_patch","input":"test patch"}`,
			output: `{"type":"custom_tool_call_output","call_id":"fixture","cache_control":{"type":"ephemeral","ttl":"1h"},"output":[
				{"type":"input_text","text":"first","cache_control":{"type":"ephemeral","ttl":"1h"}},
				{"type":"input_text","text":"second","cache_control":{"type":"ephemeral","ttl":"5m"}},
				{"type":"input_text","text":"third","cache_control":{"type":"unknown","ttl":"1h"}}
			]}`,
			wantTTL:    "5m",
			wantNested: true,
		},
		{
			name:     "invalid marker omitted",
			toolCall: `{"type":"function_call","call_id":"fixture","name":"lookup","arguments":"{}"}`,
			output:   `{"type":"function_call_output","call_id":"fixture","output":"result","cache_control":{"type":"unknown","ttl":"1h"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []byte(`{"model":"claude-sonnet-5-5","input":[{"role":"user","content":"use the tool"},` +
				tc.toolCall + "," + tc.output + "]}")
			out, err := ConvertOpenAIResponsesRequestToClaudeChecked("claude-sonnet-5-5", input, false)
			if err != nil {
				t.Fatal(err)
			}
			result := gjson.GetBytes(out, `messages.#(role=="user")#`).Array()
			block := result[len(result)-1].Get(`content.#(type=="tool_result")`)
			if block.Get("tool_use_id").String() != "fixture" {
				t.Fatalf("paired tool result missing: %s", out)
			}
			cc := block.Get("cache_control")
			if got := cc.Get("ttl").String(); got != tc.wantTTL || (tc.wantTTL != "" && cc.Get("type").String() != "ephemeral") {
				t.Fatalf("tool result cache_control = %s, want ephemeral TTL %q", cc.Raw, tc.wantTTL)
			}
			if tc.wantTTL == "" && cc.Exists() {
				t.Fatalf("invalid marker was retained: %s", cc.Raw)
			}
			content := block.Get("content")
			if tc.wantNested {
				parts := content.Array()
				if len(parts) != 3 || parts[1].Get("text").String() != "second" {
					t.Fatalf("unexpected tool content: %s", content.Raw)
				}
				for _, part := range parts {
					if part.Get("cache_control").Exists() {
						t.Fatalf("cache marker nested inside tool_result: %s", part.Raw)
					}
				}
			} else if content.String() != "result" {
				t.Fatalf("tool content = %s, want result", content.Raw)
			}
		})
	}
}
