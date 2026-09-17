package responses

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCappedToolNamesPreserveAdditionalToolsAndCustomRoundTrip(t *testing.T) {
	namespaceA := "mcp__first__" + strings.Repeat("x", 50)
	namespaceB := "mcp__second__" + strings.Repeat("x", 50)
	request := []byte(fmt.Sprintf(`{
		"tools":[{"type":"namespace","name":%q,"tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object"}}]}],
		"input":[
			{"type":"additional_tools","tools":[
				{"type":"namespace","name":%q,"tools":[{"type":"custom","name":"apply_patch"}]},
				{"type":"namespace","name":%q,"tools":[{"type":"custom","name":"apply_patch"}]}
			]},
			{"type":"custom_tool_call","namespace":%q,"name":"apply_patch","call_id":"call_custom","input":"patch text"},
			{"type":"custom_tool_call_output","call_id":"orphan","output":[{"type":"input_text","text":"task context"},{"type":"input_image","image_url":"https://example.invalid/context.png"}]},
			{"type":"custom_tool_call_output","call_id":"call_custom","output":"patched"}
		],
		"tool_choice":{"type":"custom","namespace":%q,"name":"apply_patch"}
	}`, namespaceA, namespaceB, namespaceA, namespaceB, namespaceB))
	converted := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("compat-model", request, false)
	tools := gjson.GetBytes(converted, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("expected two distinct tools after deduplication: %s", converted)
	}
	aliasA, aliasB := tools[0].Get("function.name").String(), tools[1].Get("function.name").String()
	if aliasA == aliasB || len(aliasA) > 64 || len(aliasB) > 64 || aliasA == "" || aliasB == "" {
		t.Fatalf("invalid capped aliases %q and %q", aliasA, aliasB)
	}
	if tools[0].Get("function.parameters.properties.input").Exists() || !tools[1].Get("function.parameters.properties.input").Exists() {
		t.Fatalf("deduplication changed function/custom classification: %s", converted)
	}
	if gjson.GetBytes(converted, "tool_choice.function.name").String() != aliasB {
		t.Fatalf("forced custom tool no longer matches its declaration: %s", converted)
	}
	messages := gjson.GetBytes(converted, "messages").Array()
	if len(messages) != 3 || messages[0].Get("tool_calls.0.function.name").String() != aliasB || messages[1].Get("tool_call_id").String() != "call_custom" || messages[1].Get("content").String() != "patched" {
		t.Fatalf("replayed custom call/result pair changed: %s", converted)
	}
	if messages[2].Get("role").String() != "user" || messages[2].Get("content.0.text").String() != "task context" || messages[2].Get("content.1.image_url.url").String() != "https://example.invalid/context.png" {
		t.Fatalf("orphan output lost its text/image content: %s", converted)
	}

	assertItem := func(item gjson.Result, complete bool) {
		callID := item.Get("call_id").String()
		wantType, wantNamespace := "function_call", namespaceA
		switch callID {
		case "call_function":
		case "call_custom":
			wantType, wantNamespace = "custom_tool_call", namespaceB
		default:
			t.Fatalf("unexpected tool call: %s", item.Raw)
		}
		if item.Get("type").String() != wantType || item.Get("namespace").String() != wantNamespace || item.Get("name").String() != "apply_patch" {
			t.Fatalf("tool identity changed after round trip: %s", item.Raw)
		}
		if complete {
			if callID == "call_custom" && (item.Get("input").String() != "patch text" || item.Get("arguments").Exists()) {
				t.Fatalf("custom input changed after round trip: %s", item.Raw)
			}
			if callID == "call_function" && item.Get("arguments").String() != `{"command":"pwd"}` {
				t.Fatalf("function arguments changed after round trip: %s", item.Raw)
			}
		}
	}
	toolCalls := fmt.Sprintf(`[
		{"index":0,"id":"call_function","type":"function","function":{"name":%q,"arguments":%q}},
		{"index":1,"id":"call_custom","type":"function","function":{"name":%q,"arguments":%q}}
	]`, aliasA, `{"command":"pwd"}`, aliasB, `{"input":"patch text"}`)
	raw := []byte(fmt.Sprintf(`{"id":"reply","choices":[{"message":{"role":"assistant","tool_calls":%s},"finish_reason":"tool_calls"}]}`, toolCalls))
	reply := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "compat-model", request, nil, raw, nil)
	output := gjson.GetBytes(reply, "output").Array()
	if len(output) != 2 {
		t.Fatalf("expected two non-streaming tool calls: %s", reply)
	}
	for _, item := range output {
		assertItem(item, true)
	}

	var state any
	counts := make(map[string]int)
	for _, line := range []string{
		fmt.Sprintf(`data: {"id":"reply","choices":[{"index":0,"delta":{"tool_calls":%s},"finish_reason":"tool_calls"}]}`, toolCalls),
		"data: [DONE]",
	} {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "compat-model", request, nil, []byte(line), &state) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			counts[event]++
			switch event {
			case "response.output_item.added", "response.output_item.done":
				assertItem(data.Get("item"), event == "response.output_item.done")
			case "response.completed":
				items := data.Get("response.output").Array()
				if len(items) != 2 {
					t.Fatalf("expected two completed tool calls: %s", chunk)
				}
				for _, item := range items {
					assertItem(item, true)
				}
			}
		}
	}
	if counts["response.output_item.added"] != 2 || counts["response.output_item.done"] != 2 || counts["response.completed"] != 1 {
		t.Fatalf("unexpected terminal/tool event counts: %v", counts)
	}
}
