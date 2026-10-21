package responses

import (
	"github.com/tidwall/gjson"
	"testing"
)

func TestConvertOpenAIResponsesRequestToCodexParallelToolCalls(t *testing.T) {
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
			inputJSON := []byte(`{"model":"gpt-5.6","stream":true,"store":false,` + tt.field + `"include":["reasoning.encrypted_content"],"input":[{"type":"message","role":"user","content":"hello"}]}`)
			output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)
			if got := gjson.GetBytes(output, "parallel_tool_calls"); got.Type != tt.wantType {
				t.Fatalf("parallel_tool_calls = %s (type %v), want type %v; output: %s", got.Raw, got.Type, tt.wantType, string(output))
			}
		})
	}

	t.Run("reuses normalized payload with parallel_tool_calls false", func(t *testing.T) {
		inputJSON := []byte(`{"model":"gpt-5.6","stream":true,"store":false,"parallel_tool_calls":false,"include":["reasoning.encrypted_content"],"service_tier":"priority","input":[{"type":"message","role":"user","content":"hello"}]}`)
		output := ConvertOpenAIResponsesRequestToCodex("gpt-5.6", inputJSON, true)
		if &output[0] != &inputJSON[0] {
			t.Fatal("normalized request payload with parallel_tool_calls:false was copied")
		}
		if string(output) != string(inputJSON) {
			t.Fatalf("normalized request changed:\n got: %s\nwant: %s", output, inputJSON)
		}
	})
}
