package claude_test

import (
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

func TestClaudeManualThinkingFitsOutputLimit(t *testing.T) {
	model := &registry.ModelInfo{
		ID:                  "claude-sonnet-4-5",
		Type:                "claude",
		MaxCompletionTokens: 64000,
		Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 64000, ZeroAllowed: true},
	}
	for _, tc := range []struct {
		maxTokens int
		budget    int
	}{
		{1, 0},
		{1024, 0},
		{1025, 1024},
		{4096, 2048},
	} {
		t.Run(fmt.Sprint(tc.maxTokens), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"max_tokens":%d,"thinking":{"type":"enabled","budget_tokens":2048,"display":"summarized"},"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}}`, tc.maxTokens))
			out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, model.ID, "claude", "claude", "claude", model)
			if errApply != nil {
				t.Fatal(errApply)
			}
			got := gjson.ParseBytes(out)
			if got.Get("max_tokens").Int() != int64(tc.maxTokens) || !got.Get("output_config.format").Exists() {
				t.Fatalf("output limit or format changed: %s", out)
			}
			if tc.budget == 0 {
				if got.Get("thinking.type").String() != "disabled" || got.Get("thinking.budget_tokens").Exists() || got.Get("thinking.display").Exists() {
					t.Fatalf("thinking should be disabled when the minimum cannot fit: %s", out)
				}
				return
			}
			if got.Get("thinking.type").String() != "enabled" || got.Get("thinking.budget_tokens").Int() != int64(tc.budget) {
				t.Fatalf("thinking budget should be %d: %s", tc.budget, out)
			}
		})
	}
}
