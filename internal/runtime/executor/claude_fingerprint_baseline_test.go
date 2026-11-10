package executor

import (
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestClaudeDefaultBillingVersion2321(t *testing.T) {
	out := checkSystemInstructionsWithMode([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"say ok"}]}`), false)
	system := gjson.GetBytes(out, "system").Raw
	if !strings.Contains(system, "cc_version=2.1.321") {
		t.Fatalf("default billing identity not coherent: %s", system)
	}
	if got := computeFingerprint("say ok", "2.1.321"); got != "20d" {
		t.Fatalf("fingerprint=%s", got)
	}
}
