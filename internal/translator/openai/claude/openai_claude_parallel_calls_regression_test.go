package claude

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSyncOctober21NonStreamingTool_StopReasonWithFinishReason(t *testing.T) {
	tests := []struct {
		name         string
		payload      string
		wantStopType string
	}{
		{
			name:         "tool_calls with finish_reason stop maps to tool_use",
			payload:      `{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`,
			wantStopType: "tool_use",
		},
		{
			name:         "tool_calls with finish_reason null maps to tool_use",
			payload:      `{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"finish_reason":null,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`,
			wantStopType: "tool_use",
		},
		{
			name:         "tool_calls with omitted finish_reason maps to tool_use",
			payload:      `{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`,
			wantStopType: "tool_use",
		},
		{
			name:         "tool_calls with finish_reason length stays max_tokens",
			payload:      `{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`,
			wantStopType: "max_tokens",
		},
		{
			name:         "tool_calls with finish_reason content_filter stays end_turn",
			payload:      `{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"finish_reason":"content_filter","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`,
			wantStopType: "end_turn",
		},
		{
			name:         "empty tool_calls array with finish_reason stop stays end_turn",
			payload:      `{"id":"c1","model":"gemini-3.5-flash","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"Done","tool_calls":[]}}]}`,
			wantStopType: "end_turn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", nil, nil, []byte(tt.payload), nil)
			if got := gjson.GetBytes(out, "stop_reason").String(); got != tt.wantStopType {
				t.Fatalf("ConvertOpenAIResponseToClaudeNonStream stop_reason = %q, want %q (out=%s)", got, tt.wantStopType, string(out))
			}

			var paramAny any
			emitted := ConvertOpenAIResponseToClaude(context.Background(), "", []byte(`{"stream":false}`), nil, []byte("data: "+tt.payload), &paramAny)
			if len(emitted) != 1 {
				t.Fatalf("expected 1 non-streaming chunk, got %d", len(emitted))
			}
			if got := gjson.GetBytes(emitted[0], "stop_reason").String(); got != tt.wantStopType {
				t.Fatalf("convertOpenAINonStreamingToAnthropic stop_reason = %q, want %q (out=%s)", got, tt.wantStopType, string(emitted[0]))
			}
		})
	}
}

func TestSyncOctober21StreamingTool_OmittedToolCallIndexSeparateChunksPreservesParallelCalls(t *testing.T) {
	events := runStream(t, streamReq,
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_paris","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_tokyo","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Tokyo\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":"stop"}]}`,
	)

	assertSequentialContentBlocks(t, events)

	starts := toolUseStarts(events)
	if len(starts) != 2 {
		t.Fatalf("expected two tool_use starts, got %d (starts=%+v)", len(starts), starts)
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_paris" {
		t.Fatalf("first tool id = %q, want %q", id, "call_paris")
	}
	if name := gjson.Get(starts[0].Payload, "content_block.name").String(); name != "get_weather" {
		t.Fatalf("first tool name = %q, want %q", name, "get_weather")
	}
	if id := gjson.Get(starts[1].Payload, "content_block.id").String(); id != "call_tokyo" {
		t.Fatalf("second tool id = %q, want %q", id, "call_tokyo")
	}
	if name := gjson.Get(starts[1].Payload, "content_block.name").String(); name != "get_weather" {
		t.Fatalf("second tool name = %q, want %q", name, "get_weather")
	}

	var deltas []sseEvent
	for _, e := range events {
		if e.Type == "content_block_delta" && gjson.Get(e.Payload, "delta.type").String() == "input_json_delta" {
			deltas = append(deltas, e)
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("expected two input_json_delta events, got %d (deltas=%+v)", len(deltas), deltas)
	}

	firstJSON := gjson.Get(deltas[0].Payload, "delta.partial_json").String()
	secondJSON := gjson.Get(deltas[1].Payload, "delta.partial_json").String()
	if gotCity := gjson.Get(firstJSON, "city").String(); gotCity != "Paris" {
		t.Fatalf("first tool args city = %q, want %q (json=%q)", gotCity, "Paris", firstJSON)
	}
	if gotCity := gjson.Get(secondJSON, "city").String(); gotCity != "Tokyo" {
		t.Fatalf("second tool args city = %q, want %q (json=%q)", gotCity, "Tokyo", secondJSON)
	}

	if got := countByType(events, "content_block_stop"); got != 2 {
		t.Fatalf("expected two content_block_stop events, got %d", got)
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}

func TestSyncOctober21StreamingTool_OmittedToolCallIndexSeparateChunksWithFragmentedArgs(t *testing.T) {
	events := runStream(t, streamReq,
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"id":"call_paris","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"\"Paris\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"id":"call_tokyo","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"function":{"arguments":"\"Tokyo\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)

	assertSequentialContentBlocks(t, events)

	starts := toolUseStarts(events)
	if len(starts) != 2 {
		t.Fatalf("expected two tool_use starts, got %d (starts=%+v)", len(starts), starts)
	}
	if id := gjson.Get(starts[0].Payload, "content_block.id").String(); id != "call_paris" {
		t.Fatalf("first tool id = %q, want %q", id, "call_paris")
	}
	if id := gjson.Get(starts[1].Payload, "content_block.id").String(); id != "call_tokyo" {
		t.Fatalf("second tool id = %q, want %q", id, "call_tokyo")
	}

	var deltas []sseEvent
	for _, e := range events {
		if e.Type == "content_block_delta" && gjson.Get(e.Payload, "delta.type").String() == "input_json_delta" {
			deltas = append(deltas, e)
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("expected two input_json_delta events, got %d (deltas=%+v)", len(deltas), deltas)
	}
	if gotCity := gjson.Get(gjson.Get(deltas[0].Payload, "delta.partial_json").String(), "city").String(); gotCity != "Paris" {
		t.Fatalf("first tool args city = %q, want %q", gotCity, "Paris")
	}
	if gotCity := gjson.Get(gjson.Get(deltas[1].Payload, "delta.partial_json").String(), "city").String(); gotCity != "Tokyo" {
		t.Fatalf("second tool args city = %q, want %q", gotCity, "Tokyo")
	}
	if got := lastStopReason(events); got != "tool_use" {
		t.Fatalf("stop_reason = %q, want %q", got, "tool_use")
	}
}
