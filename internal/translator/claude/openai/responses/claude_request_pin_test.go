package responses

import (
	"bytes"
	"context"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

// The request body is picked and validated once when the per-stream state is
// created; later chunks reuse that pinned copy for tool metadata and the
// completed event instead of re-picking (and re-validating) the full body on
// every chunk. Regression test for issue #6665.
func TestConvertClaudeResponseToOpenAIResponses_PinsRequestPerStream(t *testing.T) {
	originalRequest := []byte(`{
		"model":"gpt-test",
		"instructions":"pinned-instructions",
		"input":[{"type":"additional_tools","role":"developer","tools":[
			{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}
		]}]
	}`)
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_pinned","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_pinned","name":"functions__exec","input":{}}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"input\":\"pwd\"}"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var added, completed gjson.Result
	for i, chunk := range chunks {
		// Only the first chunk carries the request copy; the pinned state
		// must keep tool metadata and completed-event fields working even
		// when later chunks do not carry the request again.
		var requestForChunk []byte
		if i == 0 {
			requestForChunk = originalRequest
		}
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", requestForChunk, nil, chunk, &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			switch event {
			case "response.output_item.added":
				if data.Get("item.type").String() == "custom_tool_call" {
					added = data
				}
			case "response.completed":
				completed = data
			}
		}
	}

	if !added.Exists() || !completed.Exists() {
		t.Fatalf("missing lifecycle events: added=%v completed=%v", added.Exists(), completed.Exists())
	}
	if got := added.Get("item.name").String(); got != "exec" {
		t.Fatalf("added name = %q, want exec", got)
	}
	if got := added.Get("item.namespace").String(); got != "functions" {
		t.Fatalf("added namespace = %q, want functions", got)
	}
	if got := completed.Get("response.instructions").String(); got != "pinned-instructions" {
		t.Fatalf("completed instructions = %q, want pinned-instructions", got)
	}
}

func BenchmarkConvertClaudeResponseToOpenAIResponses_TextDelta(b *testing.B) {
	// A 64 KiB request body, as a Codex session with a long history sends.
	request := []byte(`{"model":"claude-test","instructions":"` + strings.Repeat("x", 64*1024) + `","tools":[{"name":"shell","description":"run","input_schema":{"type":"object"}}]}`)
	start := []byte(`data: {"type":"message_start","message":{"id":"msg_1"}}`)
	blockStart := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	delta := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"token "}}`)

	var param any
	ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", request, nil, start, &param)
	ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", request, nil, blockStart, &param)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", request, nil, delta, &param)
	}
}
func TestClaudeRequestPinOwnsSnapshotAndFallback(t *testing.T) {
	for _, validOriginal := range []bool{true, false} {
		t.Run(map[bool]string{true: "original", false: "fallback"}[validOriginal], func(t *testing.T) {
			request := []byte(`{"instructions":"owned-request","tools":[{"type":"custom","name":"exec"}]}`)
			original, working := request, []byte(`{"instructions":"working"}`)
			if !validOriginal {
				original = []byte(`{"broken":`)
				working = request
			}
			var state any
			ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", original, working, []byte(`data: {"type":"message_start","message":{"id":"owned"}}`), &state)
			copy(request, bytes.Repeat([]byte(" "), len(request)))
			var completed gjson.Result
			for _, out := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", []byte(`{"instructions":"changed"}`), nil, []byte(`data: {"type":"message_stop"}`), &state) {
				event, data := parseClaudeResponsesSSEEvent(t, out)
				if event == "response.completed" {
					completed = data
				}
			}
			if got := completed.Get("response.instructions").String(); got != "owned-request" {
				t.Fatalf("instructions=%q", got)
			}
			var separate any
			for _, out := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", []byte(`{"instructions":"separate"}`), nil, []byte(`data: {"type":"message_stop"}`), &separate) {
				event, data := parseClaudeResponsesSSEEvent(t, out)
				if event == "response.completed" && data.Get("response.instructions").String() != "separate" {
					t.Fatal("stream state leaked")
				}
			}
		})
	}
}
