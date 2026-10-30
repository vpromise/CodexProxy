package util

import "testing"

func TestClaudeInlineToolAdditionNameMap(t *testing.T) {
	for _, tt := range []struct{ name, request, input, want string }{
		{"inline-only", `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"SubagentHandback"}}}]}]}`, "subagenthandback", "SubagentHandback"},
		{"escaped-marker", `{"messages":[{"role":"system","content":[{"type":"tool_\u0061ddition","tool":{"type":"tool_definition","definition":{"name":"SubagentHandback"}}}]}]}`, "subagenthandback", "SubagentHandback"},
		{"existing-first-match", `{"tools":[{"name":"Read"}],"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"definition":{"name":"read"}}}]}]}`, "read", "Read"},
		{"user-is-not-system", `{"messages":[{"role":"user","content":[{"type":"tool_addition","tool":{"definition":{"name":"SubagentHandback"}}}]}]}`, "subagenthandback", "subagenthandback"},
		{"reference-is-not-definition", `{"messages":[{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_reference","name":"SubagentHandback"}}]}]}`, "subagenthandback", "subagenthandback"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := MapToolName(ToolNameMapFromClaudeRequest([]byte(tt.request)), tt.input); got != tt.want {
				t.Fatalf("mapped name = %q, want %q", got, tt.want)
			}
		})
	}
}
