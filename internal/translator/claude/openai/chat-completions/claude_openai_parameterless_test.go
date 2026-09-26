package chat_completions

import (
	"reflect"
	"testing"

	"github.com/tidwall/gjson"
)

func TestParameterlessFunctionToolGetsObjectSchema(t *testing.T) {
	for _, tc := range []struct{ schema, want string }{
		{``, `{"type":"object","properties":{}}`},
		{`,"parameters":{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`, `{"type":"object","properties":{"id":{"type":"integer"}},"required":["id"]}`},
		{`,"parametersJsonSchema":{"type":"object","properties":{"name":{"type":"string"}}}`, `{"type":"object","properties":{"name":{"type":"string"}}}`},
	} {
		input := []byte(`{"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"get_status"` + tc.schema + `}}],"tool_choice":{"type":"function","function":{"name":"get_status"}}}`)
		out := ConvertOpenAIRequestToClaude("claude-sonnet-4-6", input, false)
		if got := gjson.GetBytes(out, "tools.0.input_schema").Raw; !reflect.DeepEqual(gjson.Parse(got).Value(), gjson.Parse(tc.want).Value()) {
			t.Errorf("schema = %s, want %s", got, tc.want)
		}
		if got := gjson.GetBytes(out, "tool_choice.name").String(); got != "get_status" {
			t.Errorf("choice = %q", got)
		}
	}
}
