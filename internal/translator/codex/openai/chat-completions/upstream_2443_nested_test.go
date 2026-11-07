package chat_completions

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNestedCustomToolDeclaration(t *testing.T) {
	for _, tt := range []struct {
		name   string
		format string
		want   string
	}{
		{"grammar", `,"format":{"type":"grammar","grammar":{"syntax":"lark","definition":"start: patch"}}`, `{"type":"grammar","syntax":"lark","definition":"start: patch"}`},
		{"text", `,"format":{"type":"text"}`, `{"type":"text"}`},
		{"default", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			input := []byte(`{
				"messages":[
					{"role":"assistant","tool_calls":[{"id":"call_patch","type":"function","function":{"name":"apply_patch","arguments":"patch"}}]},
					{"role":"tool","tool_call_id":"call_patch","content":"done"}
				],
				"tools":[
					{"type":"function","function":{"name":"bash"}},
					{"type":"function","function":{"name":"read_bash"}},
					{"type":"function","function":{"name":"stop_bash"}},
					{"type":"function","function":{"name":"list_bash"}},
					{"type":"custom","custom":{"name":"apply_patch","description":"Edit files"` + tt.format + `}}
				],
				"tool_choice":{"type":"custom","custom":{"name":"apply_patch"}}
			}`)
			out, errConvert := ConvertOpenAIRequestToCodexChecked("codex-test", input, true)
			if errConvert != nil {
				t.Fatal(errConvert)
			}
			tool := gjson.GetBytes(out, "tools.4")
			if tool.Get("custom").Exists() {
				t.Fatalf("unknown parameter tools[4].custom would be sent upstream: %s", tool.Raw)
			}
			if tool.Get("type").String() != "custom" || tool.Get("name").String() != "apply_patch" || tool.Get("description").String() != "Edit files" {
				t.Fatalf("unexpected custom declaration: %s", tool.Raw)
			}
			if got, want := tool.Get("format").Value(), gjson.Parse(tt.want).Value(); !reflect.DeepEqual(got, want) {
				t.Fatalf("format = %v, want %v", got, want)
			}
			if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "apply_patch" {
				t.Fatalf("tool_choice.name = %q, want apply_patch", got)
			}
			if got := gjson.GetBytes(out, "input.0.type").String(); got != "custom_tool_call" {
				t.Fatalf("history call type = %q, want custom_tool_call", got)
			}
			if got := gjson.GetBytes(out, "input.0.input").String(); got != "patch" {
				t.Fatalf("history input = %q, want patch", got)
			}
			if got := gjson.GetBytes(out, "input.1.type").String(); got != "custom_tool_call_output" {
				t.Fatalf("history output type = %q, want custom_tool_call_output", got)
			}
		})
	}
}

func TestNestedCustomToolNameShortening(t *testing.T) {
	name := strings.Repeat("custom_tool_", 7)
	input := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"call_long","type":"function","function":{"name":"` + name + `","arguments":"raw"}}]},{"role":"tool","tool_call_id":"call_long","content":"ok"}],"tools":[{"type":"custom","custom":{"name":"` + name + `"}}],"tool_choice":{"type":"custom","custom":{"name":"` + name + `"}}}`)
	out, errConvert := ConvertOpenAIRequestToCodexChecked("codex-test", input, true)
	if errConvert != nil {
		t.Fatal(errConvert)
	}
	shortName := gjson.GetBytes(out, "tools.0.name").String()
	if shortName == "" || shortName == name || len(shortName) > 64 {
		t.Fatalf("unexpected shortened custom name: %q", shortName)
	}
	if got := gjson.GetBytes(out, "tool_choice.name").String(); got != shortName {
		t.Fatalf("tool_choice.name = %q, want %q", got, shortName)
	}
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "custom_tool_call" {
		t.Fatalf("input.0.type = %q, want custom_tool_call", got)
	}
	if got := gjson.GetBytes(out, "input.0.name").String(); got != shortName {
		t.Fatalf("input.0.name = %q, want %q", got, shortName)
	}
	if got := buildReverseMapFromOriginalOpenAI(input)[shortName]; got != name {
		t.Fatalf("reverse mapping = %q, want %q", got, name)
	}
}
