package executor

import (
	"strings"
	"testing"
)

func TestClaude258AdvancedToolBetaFollowsFeatures(t *testing.T) {
	for _, tt := range []struct {
		name string
		tool string
		want bool
	}{
		{"plain inline tool", `{"name":"Read","input_schema":{"type":"object"}}`, false},
		{"disabled deferral", `{"name":"Read","defer_loading":false}`, false},
		{"tool search", `{"type":"tool_search_tool_regex_20251119","name":"search"}`, true},
		{"deferred tool", `{"name":"Read","defer_loading":true}`, true},
		{"input examples", `{"name":"Read","input_examples":[{"path":"a.go"}]}`, true},
		{"programmatic caller", `{"name":"Read","allowed_callers":["code_execution_20250825"]}`, true},
		{"ordinary property named allowed_callers", `{"name":"Read","input_schema":{"type":"object","properties":{"allowed_callers":{"type":"string"}}}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5","tools":[` + tt.tool + `]}`)
			betas := claudeCodeCLIBetas(body, map[string]bool{claudeAdvisorToolBeta: true}, false)
			if got := strings.Contains(betas, claudeAdvancedToolUseBeta); got != tt.want {
				t.Fatalf("advanced tool beta present = %v, want %v: %s", got, tt.want, betas)
			}
			if tt.want && !strings.Contains(betas, claudeAdvisorToolBeta+","+claudeAdvancedToolUseBeta+","+claudeEffortBeta) {
				t.Fatalf("unexpected capability order: %s", betas)
			}
			if !strings.Contains(betas, claudeContext1MBeta) || !strings.Contains(betas, claudeMidConvSystemBeta) {
				t.Fatalf("local context/system policy lost: %s", betas)
			}
		})
	}
}

func TestClaude258AFKBetaIsRequestedAndOrdered(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5","speed":"fast"}`)
	betas := claudeCodeCLIBetas(body, map[string]bool{claudeAFKModeBeta: true}, true)
	if !strings.Contains(betas, claudeFastModeBeta+","+claudeAFKModeBeta+","+claudeExtendedCacheTTLBeta) {
		t.Fatalf("unexpected AFK capability order: %s", betas)
	}
	if got := claudeCodeCLIBetas(body, nil, true); strings.Contains(got, claudeAFKModeBeta) {
		t.Fatalf("unsolicited AFK beta: %s", got)
	}
}
