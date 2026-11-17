package helps

import (
	"strings"
	"testing"
)

func TestClaudeOutputFormatTokenAccounting(t *testing.T) {
	const base = `{"messages":[{"role":"user","content":"hello"}]}`
	baseCount, err := CountClaudeInputTokens([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	effortCount, err := CountClaudeInputTokens([]byte(strings.TrimSuffix(base, "}") + `,"output_config":{"effort":"high"}}`))
	if err != nil || effortCount != baseCount {
		t.Fatalf("control-only effort counted: %d / %d (%v)", effortCount, baseCount, err)
	}
	const schema = `{"type":"object","properties":{"value":{"description":"a detailed structured field","enum":[9007199254740993]}}}`
	payload := []byte(strings.TrimSuffix(base, "}") + `,"output_config":{"effort":"high","format":{"type":"json_schema","schema":` + schema + `}}}`)
	segments, err := collectClaudeInputTokenSegments(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(segments, "\n"), schema) {
		t.Fatalf("schema precision/content lost: %q", segments)
	}
	count, err := CountClaudeInputTokens(payload)
	if err != nil || count <= baseCount {
		t.Fatalf("schema tokens missing: %d / %d (%v)", count, baseCount, err)
	}
}
