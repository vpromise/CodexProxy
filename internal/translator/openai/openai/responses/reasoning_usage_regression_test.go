package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSyncOctober21ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream_MapsCompletionTokensDetailsReasoningTokens(t *testing.T) {
	request := []byte(`{"model":"mercury-3"}`)
	for _, tc := range []struct {
		name      string
		usageJSON string
		want      int64
	}{
		{
			name:      "completion_tokens_details.reasoning_tokens",
			usageJSON: `{"prompt_tokens":9,"completion_tokens":99,"total_tokens":108,"completion_tokens_details":{"reasoning_tokens":60}}`,
			want:      60,
		},
		{
			name:      "output_tokens_details.reasoning_tokens",
			usageJSON: `{"prompt_tokens":9,"completion_tokens":99,"total_tokens":108,"output_tokens_details":{"reasoning_tokens":45}}`,
			want:      45,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"id":"chatcmpl_reasoning","object":"chat.completion","created":1773896263,"model":"mercury-3","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":` + tc.usageJSON + `}`)
			resp := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "mercury-3", request, request, raw, nil)
			data := gjson.ParseBytes(resp)

			reasoning := data.Get("usage.output_tokens_details.reasoning_tokens")
			if !reasoning.Exists() {
				t.Fatalf("expected usage.output_tokens_details.reasoning_tokens to exist, got usage=%s", data.Get("usage").Raw)
			}
			if got := reasoning.Int(); got != tc.want {
				t.Fatalf("usage.output_tokens_details.reasoning_tokens = %d, want %d; usage=%s", got, tc.want, data.Get("usage").Raw)
			}
		})
	}
}

func TestSyncOctober21ResponsesReasoningZeroKeepsNativePrecedence(t *testing.T) {
	raw := []byte(`{"id":"c","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":5,"total_tokens":8,"output_tokens_details":{"reasoning_tokens":0},"completion_tokens_details":{"reasoning_tokens":4}}}`)
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "test", nil, nil, raw, nil)
	got := gjson.GetBytes(out, "usage.output_tokens_details.reasoning_tokens")
	if !got.Exists() || got.Int() != 0 {
		t.Fatalf("lost explicit zero: %s", out)
	}
}
