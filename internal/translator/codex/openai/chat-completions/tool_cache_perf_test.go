package chat_completions

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func tcRequest(historyCalls int) []byte {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	var msgs []string
	msgs = append(msgs, fmt.Sprintf(`{"role":"user","content":%q}`, strings.Repeat("context ", 4096)))
	for i := 0; i < historyCalls; i++ {
		msgs = append(msgs, fmt.Sprintf(`{"role":"assistant","tool_calls":[{"id":"h%d","type":"function","function":{"name":%q,"arguments":"{}"}}]}`, i, longName))
		msgs = append(msgs, fmt.Sprintf(`{"role":"tool","tool_call_id":"h%d","content":"ok"}`, i))
	}
	return []byte(fmt.Sprintf(`{"model":"gpt-5","messages":[%s],"tools":[{"type":"function","function":{"name":%q}},{"type":"function","function":{"name":"plain"}},{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}},{"type":"function","function":{"name":"apply_patch"}}]}`,
		strings.Join(msgs, ","), longName))
}

func tcStreamEvents(shortLong string) [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp-1","created_at":1700000000,"model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"hi"}`),
		[]byte(fmt.Sprintf(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_1","name":%q,"status":"in_progress"}}`, shortLong)),
		[]byte(`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","call_id":"call_1","delta":"{\"path\":\"/a\"}"}`),
		[]byte(fmt.Sprintf(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":%q,"arguments":"{\"path\":\"/a\"}","status":"completed"}}`, shortLong)),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_2","name":"plain","arguments":"{}","status":"completed"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","call_id":"call_3","name":"apply_patch","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.custom_tool_call_input.delta","item_id":"ctc_3","call_id":"call_3","delta":"*** Begin Patch\n*** End Patch"}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"call_3","name":"apply_patch","input":"*** Begin Patch\n*** End Patch","status":"completed"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_4","name":"unknown_tool","status":"in_progress"}}`),
		[]byte(`data: {"type":"response.completed","response":{"id":"resp-1","status":"completed","usage":{"input_tokens":10,"output_tokens":5}}}`),
	}
}

func tcCases() []struct {
	name string
	req  []byte
} {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	_ = longName
	return []struct {
		name string
		req  []byte
	}{
		{"nil", nil},
		{"malformed", []byte("{")},
		{"no-tools", []byte(`{"messages":[]}`)},
		{"full-history", tcRequest(6)},
		{"tools-object", []byte(`{"messages":[],"tools":{"type":"function","function":{"name":"apply_patch"}}}`)},
		{"custom-only", []byte(`{"messages":[],"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`)},
	}
}

func BenchmarkCodexChatToolStreamLongHistory(b *testing.B) {
	longName := "mcp__very_long_server_name_for_testing__" + strings.Repeat("a", 40)
	request := tcRequest(60)
	shortLong := buildShortNameMap(collectRequestToolNames(request))[longName]
	events := tcStreamEvents(shortLong)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var param any
		for _, e := range events {
			ConvertCodexResponseToOpenAI(context.Background(), "gpt-5", request, request, e, &param)
		}
	}
}

// The cached metadata must answer exactly like the legacy per-call rescans.
func TestCodexChatToolMetadataEquivalence(t *testing.T) {
	// Compare every cached lookup with the existing request/history name map.
	requests := [][]byte{
		nil, []byte("{"), []byte(`{"messages":[]}`),
		tcRequest(3),
		[]byte(`{"messages":[],"tools":{"type":"function","function":{"name":"apply_patch"}}}`),
		[]byte(`{"messages":[],"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`),
		[]byte(`{"messages":[],"tools":[{"type":"function","function":{"name":"apply_patch"}},{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`),
		[]byte(`{"messages":[],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}]}`),
		[]byte(`{"messages":[],"tools":[{"type":"namespace","name":"editor","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}]}`),
	}
	names := []string{"", "plain", "apply_patch", "unknown", "editor__apply_patch"}
	for ri, req := range requests {
		var metadata codexChatToolMetadata
		rev := buildReverseMapFromOriginalOpenAI(req)
		for _, name := range names {
			want := name
			if orig, ok := rev[name]; ok {
				want = orig
			}
			if got := metadata.restoreToolName(req, name); got != want {
				t.Errorf("req %d restore(%q) = %q, want %q", ri, name, got, want)
			}
		}

	}
}
