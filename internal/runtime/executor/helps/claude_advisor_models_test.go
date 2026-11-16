package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeClaudeAdvisorToolModels(t *testing.T) {
	input := []byte(`{"large":9007199254740993,"tools":[{"type":"advisor_20260301","model":" claude-opus-5-5(high) "},{"type":"ADVISOR_20260401","model":"claude-sonnet-5-5(custom)"},{"type":"advisor_20260301","model":"claude-haiku-4-5-20251001(8192)"},{"type":"advisor_20260301","model":""},{"type":"advisor_20260301","model":"   "},{"type":"advisor_20260301","model":"(high)"},{"type":"advisor_20260301","model":123},{"type":"advisor_20260301","model":null},{"type":"custom","name":"advisor","model":"keep-me(high)"}],"messages":[{"role":"user","content":"keep-me(high)"}]}`)
	original := bytes.Clone(input)
	output := NormalizeClaudeAdvisorToolModels(input)
	for path, want := range map[string]string{"tools.0.model": "claude-opus-5-5", "tools.1.model": "claude-sonnet-5-5", "tools.2.model": "claude-haiku-4-5-20251001", "tools.3.model": "", "tools.4.model": "   ", "tools.5.model": "(high)", "tools.8.model": "keep-me(high)", "messages.0.content": "keep-me(high)"} {
		if got := gjson.GetBytes(output, path).String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if gjson.GetBytes(output, "large").Raw != "9007199254740993" || gjson.GetBytes(output, "tools.6.model").Raw != "123" || gjson.GetBytes(output, "tools.7.model").Type != gjson.Null {
		t.Fatal("unrelated JSON values changed")
	}
	if !bytes.Equal(input, original) {
		t.Fatal("caller payload mutated")
	}
	if !bytes.Equal(NormalizeClaudeAdvisorToolModels(output), output) {
		t.Fatal("normalization is not idempotent")
	}
	for _, body := range [][]byte{nil, []byte(`{}`), []byte(`{"tools":{}}`), []byte(`{"tools":[{"type":"advisor_20260301","model":"claude-opus-5-5"}]}`), []byte(`{"tools":[null,1]}`)} {
		if got := NormalizeClaudeAdvisorToolModels(body); !bytes.Equal(got, body) {
			t.Fatalf("unrelated payload changed: %s", got)
		}
	}
}
