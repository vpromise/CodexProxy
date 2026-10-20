package responses

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSyncOctober21StopReasonErrorsKeepOutput(t *testing.T) {
	for _, tc := range []struct {
		reason string
		event  string
	}{
		{"refusal", "response.incomplete"},
		{"model_context_window_exceeded", "response.failed"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			chunks := [][]byte{
				[]byte(`data: {"type":"message_start","message":{"id":"msg_e","usage":{"input_tokens":7,"output_tokens":0,"cache_read_input_tokens":3}}}`),
				[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
				[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Some text."}}`),
				[]byte(`data: {"type":"content_block_stop","index":0}`),
				[]byte(fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":4}}`, tc.reason)),
				[]byte(`data: {"type":"message_stop"}`),
			}
			var order []string
			var terminal gjson.Result
			for _, output := range translateClaudeResponsesStreamThroughRegistry(chunks) {
				event, data := parseClaudeResponsesSSEEvent(t, output)
				if event == "response.output_item.done" && data.Get("item.type").String() == "message" {
					order = append(order, "message_done:"+data.Get("item.content.0.text").String())
				}
				if event == tc.event {
					order = append(order, event)
					terminal = data.Get("response")
				}
				if event == "response.completed" {
					t.Fatalf("unexpected response.completed for %s", tc.reason)
				}
			}
			if got := strings.Join(order, "|"); got != "message_done:Some text.|"+tc.event {
				t.Fatalf("event order = %s", got)
			}
			if terminal.Get("usage.input_tokens").Int() != 10 || terminal.Get("usage.input_tokens_details.cached_tokens").Int() != 3 || terminal.Get("usage.output_tokens").Int() != 4 {
				t.Fatalf("usage = %s", terminal.Get("usage").Raw)
			}
		})
	}
}

func TestSyncOctober21ResponsesParallelToolCallsFalseMapsToDisableParallelToolUse(t *testing.T) {
	tools := `[{"type":"function","name":"shell","parameters":{"type":"object","properties":{}}}]`
	for _, tc := range []struct {
		name    string
		request string
		want    string
	}{
		{"no tool_choice", `{"model":"claude-test","parallel_tool_calls":false,"tools":` + tools + `,"input":"hi"}`, `{"type":"auto","disable_parallel_tool_use":true}`},
		{"auto", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":"auto","tools":` + tools + `,"input":"hi"}`, `{"type":"auto","disable_parallel_tool_use":true}`},
		{"required", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":"required","tools":` + tools + `,"input":"hi"}`, `{"type":"any","disable_parallel_tool_use":true}`},
		{"forced tool", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"function","name":"shell"},"tools":` + tools + `,"input":"hi"}`, `{"name":"shell","type":"tool","disable_parallel_tool_use":true}`},
		{"none", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":"none","tools":` + tools + `,"input":"hi"}`, ``},
		{"no tools", `{"model":"claude-test","parallel_tool_calls":false,"input":"hi"}`, ``},
		{"forced tool filtered out", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"function","name":"missing"},"tools":` + tools + `,"input":"hi"}`, ``},
		{"builtin web_search present", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"web_search"},"tools":[{"type":"web_search"}],"input":"hi"}`, `{"type":"auto","disable_parallel_tool_use":true}`},
		{"builtin web_search filtered out", `{"model":"claude-test","parallel_tool_calls":false,"tool_choice":{"type":"web_search"},"tools":` + tools + `,"input":"hi"}`, ``},
		{"true", `{"model":"claude-test","parallel_tool_calls":true,"tools":` + tools + `,"input":"hi"}`, ``},
		{"absent", `{"model":"claude-test","tools":` + tools + `,"input":"hi"}`, ``},
		{"true with auto", `{"model":"claude-test","parallel_tool_calls":true,"tool_choice":"auto","tools":` + tools + `,"input":"hi"}`, `{"type":"auto"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := ConvertOpenAIResponsesRequestToClaude("claude-test", []byte(tc.request), true)
			if got := gjson.GetBytes(out, "tool_choice").Raw; got != tc.want {
				t.Fatalf("tool_choice = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSyncOctober21ContextWindowExceededNonStreamIsFailedWithError(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_c","usage":{"input_tokens":5,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_delta","delta":{"stop_reason":"model_context_window_exceeded"},"usage":{"output_tokens":2}}`),
		[]byte(`data: {"type":"message_stop"}`),
	}
	output := gjson.ParseBytes(ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chunks, []byte("\n")), nil))
	if output.Get("status").String() != "failed" || output.Get("error.code").String() != "context_length_exceeded" {
		t.Fatalf("response = %s", output.Raw)
	}
	if output.Get("usage.output_tokens").Int() != 2 || output.Get("usage.input_tokens").Int() != 5 {
		t.Fatalf("usage = %s", output.Get("usage").Raw)
	}
}
