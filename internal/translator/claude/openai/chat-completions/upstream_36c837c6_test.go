package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestOpenAIChatToClaudeOptionalSamplingFields(t *testing.T) {
	input := []byte(`{"messages":[{"role":"user","content":"hi"}],"max_tokens":null,"max_completion_tokens":17,"top_p":null,"stop":[null,"",false,"END"]}`)
	out, err := ConvertOpenAIRequestToClaudeChecked("claude-test", input, false)
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(out, "max_tokens").Int() != 17 || gjson.GetBytes(out, "top_p").Exists() || gjson.GetBytes(out, "stop_sequences").Raw != `["END"]` {
		t.Fatalf("unexpected converted request: %s", out)
	}
}
