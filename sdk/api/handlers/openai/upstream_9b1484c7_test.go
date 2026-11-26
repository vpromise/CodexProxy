package openai

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestLegacyCompletionOptionalScalars(t *testing.T) {
	raw := []byte(`{"prompt":"hi","max_tokens":null,"temperature":null,"top_p":null,"frequency_penalty":null,"presence_penalty":null,"stop":null,"stream":null,"logprobs":null,"top_logprobs":null,"echo":null}`)
	out := convertCompletionsRequestToChatCompletions(raw)
	for _, field := range []string{"max_tokens", "temperature", "top_p", "frequency_penalty", "presence_penalty", "stop", "stream", "logprobs", "top_logprobs", "echo"} {
		if gjson.GetBytes(out, field).Exists() {
			t.Errorf("unexpected field %s in %s", field, out)
		}
	}
	out = convertCompletionsRequestToChatCompletions([]byte(`{"prompt":"hi","temperature":0,"stream":false,"logprobs":3,"echo":false}`))
	if gjson.GetBytes(out, "temperature").Raw != "0" || gjson.GetBytes(out, "stream").Type != gjson.False || gjson.GetBytes(out, "echo").Type != gjson.False || !gjson.GetBytes(out, "logprobs").Bool() || gjson.GetBytes(out, "top_logprobs").Int() != 3 {
		t.Fatalf("wrong explicit values: %s", out)
	}
}

func TestLegacyCompletionStreamingNullUsage(t *testing.T) {
	if out := convertChatCompletionsStreamChunkToCompletions([]byte(`{"choices":[{"delta":{"role":"assistant"},"finish_reason":null}],"usage":null}`)); len(out) != 0 {
		t.Fatalf("role-only chunk with null usage was emitted: %s", out)
	}
	out := convertChatCompletionsStreamChunkToCompletions([]byte(`{"choices":[],"usage":{"total_tokens":7}}`))
	if got := gjson.GetBytes(out, "usage.total_tokens").Int(); got != 7 {
		t.Fatalf("lost usage-only chunk: %s", out)
	}
}
