package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func chatStopReasonChunks(reason string) [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_c","model":"claude-test","usage":{"input_tokens":5,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":3}}`, reason)),
		[]byte(`data: {"type":"message_stop"}`),
	}
}

func TestSyncOctober23ChatContextWindowPreservesOutput(t *testing.T) {
	for _, reason := range []string{"model_context_window_exceeded", "max_tokens"} {
		var param any
		var finish, text string
		var tokens int64
		for _, input := range chatStopReasonChunks(reason) {
			for _, output := range ConvertClaudeResponseToOpenAI(context.Background(), "claude-test", nil, nil, input, &param) {
				chunk := gjson.ParseBytes(output)
				if chunk.Get("error").Exists() {
					t.Fatalf("unexpected error: %s", output)
				}
				if x := chunk.Get("choices.0.finish_reason").String(); x != "" {
					finish = x
				}
				text += chunk.Get("choices.0.delta.content").String()
				if x := chunk.Get("usage.completion_tokens").Int(); x > 0 {
					tokens = x
				}
			}
		}
		if finish != "length" || text != "partial" || tokens != 3 {
			t.Fatalf("%s: finish=%q text=%q tokens=%d", reason, finish, text, tokens)
		}
		out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chatStopReasonChunks(reason), []byte("\n")), nil)
		if gjson.GetBytes(out, "choices.0.finish_reason").String() != "length" || gjson.GetBytes(out, "choices.0.message.content").String() != "partial" || gjson.GetBytes(out, "usage.completion_tokens").Int() != 3 {
			t.Fatalf("nonstream: %s", out)
		}
	}
}

func TestSyncOctober21ChatRefusalKeepsContentFilterAndUsage(t *testing.T) {
	var param any
	var finish string
	for _, chunk := range chatStopReasonChunks("refusal") {
		for _, output := range ConvertClaudeResponseToOpenAI(context.Background(), "claude-test", nil, nil, chunk, &param) {
			if reason := gjson.GetBytes(output, "choices.0.finish_reason").String(); reason != "" {
				finish = reason
			}
		}
	}
	if finish != "content_filter" {
		t.Fatalf("stream finish_reason = %q", finish)
	}
	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chatStopReasonChunks("refusal"), []byte("\n")), nil)
	if gjson.GetBytes(out, "choices.0.finish_reason").String() != "content_filter" || gjson.GetBytes(out, "usage.completion_tokens").Int() != 3 {
		t.Fatalf("non-stream = %s", out)
	}
}

func TestSyncOctober21ChatMaxTokensUnchanged(t *testing.T) {
	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chatStopReasonChunks("max_tokens"), []byte("\n")), nil)
	if gjson.GetBytes(out, "choices.0.finish_reason").String() != "length" {
		t.Fatalf("non-stream = %s", out)
	}
}

func TestSyncOctober23TruncatedToolCallKeepsLength(t *testing.T) {
	for _, reason := range []string{"max_tokens", "model_context_window_exceeded", "tool_use"} {
		chunks := [][]byte{[]byte(`data: {"type":"message_start","message":{"id":"msg_tool","usage":{"input_tokens":5}}}`), []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tool_1","name":"lookup","input":{}}}`), []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}`), []byte(fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":2}}`, reason)), []byte(`data: {"type":"message_stop"}`)}
		out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chunks, []byte("\n")), nil)
		want := "length"
		if reason == "tool_use" {
			want = "tool_calls"
		}
		if gjson.GetBytes(out, "choices.0.finish_reason").String() != want || gjson.GetBytes(out, "choices.0.message.tool_calls.0.function.arguments").String() != `{"q":` || gjson.GetBytes(out, "usage.completion_tokens").Int() != 2 {
			t.Fatalf("reason=%s output=%s", reason, out)
		}
	}
}
