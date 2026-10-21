package chat_completions

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestConvertOpenAIRequestToCodexParallelToolCalls(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		wantType gjson.Type
	}{
		{name: "false preserved", field: `"parallel_tool_calls":false,`, wantType: gjson.False},
		{name: "true preserved", field: `"parallel_tool_calls":true,`, wantType: gjson.True},
		{name: "null defaults to true", field: `"parallel_tool_calls":null,`, wantType: gjson.True},
		{name: "omitted defaults to true", field: "", wantType: gjson.True},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := []byte(`{"model":"gpt-6.1-sol",` + tt.field + `"messages":[{"role":"user","content":"hello"}]}`)
			output, errConvert := ConvertOpenAIRequestToCodexChecked("gpt-6.1-sol", inputJSON, true)
			if errConvert != nil {
				t.Fatalf("ConvertOpenAIRequestToCodex returned error: %v", errConvert)
			}
			if got := gjson.GetBytes(output, "parallel_tool_calls"); got.Type != tt.wantType {
				t.Fatalf("parallel_tool_calls = %s (type %v), want type %v; output: %s", got.Raw, got.Type, tt.wantType, string(output))
			}
		})
	}
}
