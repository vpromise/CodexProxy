package helps

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeCodeRequestMetadataAuthority(t *testing.T) {
	const bodyID = "11111111-2222-4333-8444-555555555555"
	const incomingID = "22222222-2222-4333-8444-555555555555"
	body := []byte(`{"system":[{"type":"text","text":"x-anthropic-billing-header: cc_prompt_id=` + bodyID + `;"}],"messages":[{"role":"user","content":"hi"}]}`)
	for _, tc := range []struct {
		name, endpoint                   string
		native                           bool
		wantPrompt, wantClass, wantTools string
	}{
		{"native", "https://api.anthropic.com/v1/messages", true, bodyID, "subagent", `[{"name":"Read"}]`},
		{"reconstructed", "https://api.anthropic.com/v1/messages", false, bodyID, "", "[]"},
		{"native count", "https://api.anthropic.com/v1/messages/count_tokens", true, incomingID, "subagent", `[{"name":"Read"}]`},
		{"reconstructed count", "https://api.anthropic.com/v1/messages/count_tokens", false, "", "", ""},
		{"gateway", "https://gateway.example/v1/messages", true, "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := http.NewRequest(http.MethodPost, tc.endpoint, nil)
			h := http.Header{"x-claude-code-prompt-id": {incomingID}, "x-claude-code-request-class": {"subagent"}, "x-claude-code-agent-type": {"Explore"}, "x-claude-code-sandy-plover": {`[{"name":"Read"}]`}}
			ApplyClaudeCodeRequestMetadata(r, body, h, tc.native, nil)
			for name, want := range map[string]string{"X-Claude-Code-Prompt-Id": tc.wantPrompt, "X-Claude-Code-Request-Class": tc.wantClass, "X-Claude-Code-Sandy-Plover": tc.wantTools} {
				if got := r.Header.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}
			if h["x-claude-code-prompt-id"][0] != incomingID {
				t.Fatal("mutated caller headers")
			}
		})
	}
	for _, payload := range []string{`{"thread":{"type":"continue","previous_message_id":"msg_previous"},"messages":[]}`, `{"max_tokens":1,"messages":[{"role":"user","content":"probe"}]}`} {
		r, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
		ApplyClaudeCodeRequestMetadata(r, []byte(payload), nil, false, nil)
		if r.Header.Get("X-Claude-Code-Sandy-Plover") != "" {
			t.Fatal("synthesized a tool history for an incomplete/helper request")
		}
	}
}

func TestClaudeCodeRequestMetadataToolAliases(t *testing.T) {
	r, _ := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
	r.Header.Set("X-Claude-Code-Request-Class", "auxiliary")
	h := http.Header{
		"X-Claude-Code-Request-Class":       {"main"},
		"X-Claude-Code-Sandy-Plover":        {`[{"name":"Bash","url":"https://private.example/","directory_entry":"private"},{"name":"Read"}]`},
		"X-Claude-Code-Prev-Tool-Durations": {"Bash=19,Read=2"},
	}
	ApplyClaudeCodeRequestMetadata(r, []byte(`{"messages":[]}`), h, true, map[string]string{"mcp__proxy__bash": "Bash"})
	if got := r.Header.Get("X-Claude-Code-Sandy-Plover"); got != `[{"name":"mcp__proxy__bash","mcp":true},{"name":"Read"}]` {
		t.Errorf("aliased tool metadata = %s", got)
	}
	if got := r.Header.Get("X-Claude-Code-Prev-Tool-Durations"); got != "mcp__proxy__bash=19,Read=2" {
		t.Errorf("aliased durations = %s", got)
	}
	if got := r.Header.Get("X-Claude-Code-Request-Class"); got != "auxiliary" {
		t.Errorf("operator class = %s", got)
	}
	if strings.Contains(h.Get("X-Claude-Code-Sandy-Plover"), "mcp__proxy") {
		t.Fatal("mutated native metadata")
	}
}

func TestClaudeUsedToolsHeader(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"first turn", `{"messages":[{"role":"user","content":"hi"}]}`, `[]`},
		{"pending tool", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash"}]}]}`, `[]`},
		{"two tools and repeated tool", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash"},{"type":"tool_use","id":"b","name":"Read"},{"type":"tool_use","id":"c","name":"Bash"},{"type":"server_tool_use","id":"s","name":"Search"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a"},{"type":"tool_result","tool_use_id":"b","is_error":true},{"type":"tool_result","tool_use_id":"c"},{"type":"tool_result","tool_use_id":"s"}]},{"role":"user","content":"next turn"}]}`, `[{"name":"Bash"},{"name":"Read"}]`},
		{"mcp", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"mcp__server__tool"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a"}]}]}`, `[{"name":"mcp__server__tool","mcp":true}]`},
		{"unicode", `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"查找😀"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a"}]}]}`, `[{"name":"\u67e5\u627e\ud83d\ude00"}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := claudeUsedToolsHeader([]byte(tc.body)); got != tc.want {
				t.Errorf("got %s, want %s", got, tc.want)
			}
		})
	}
	ordered := []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash"},{"type":"tool_use","id":"b","name":"mcp__server__tool"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"a"},{"type":"tool_result","tool_use_id":"b"}]}]}`)
	if got := claudeUsedToolsHeader(ordered); got != `[{"name":"mcp__server__tool","mcp":true},{"name":"Bash"}]` {
		t.Errorf("core tool ordering = %s", got)
	}
	for _, count := range []int{64, 65} {
		var calls, results []string
		for i := 0; i < count; i++ {
			calls = append(calls, fmt.Sprintf(`{"type":"tool_use","id":"%d","name":"Tool%d"}`, i, i))
			results = append(results, fmt.Sprintf(`{"type":"tool_result","tool_use_id":"%d"}`, i))
		}
		body := `{"messages":[{"role":"assistant","content":[` + strings.Join(calls, ",") + `]},{"role":"user","content":[` + strings.Join(results, ",") + `]}]}`
		if got := claudeUsedToolsHeader([]byte(body)); (got == "") != (count > 64) {
			t.Errorf("count %d unexpectedly returned %q", count, got)
		}
	}
}
