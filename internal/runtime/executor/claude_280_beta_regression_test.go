package executor

import (
	"net/http"
	"strings"
	"testing"
)

func TestClaude280RequestedBetasSurviveHeaderProfiles(t *testing.T) {
	betas := []string{
		"per-turn-control-2026-07-01", "timing-2026-09-09", "mid-conversation-tool-changes-2026-07-01",
		"inline-tools-2026-09-15", "mid-conversation-system-clear-at-2026-08-21", "dangerous-tool-use-2026-09-03",
		"thinking-binding-controls-2026-08-01", "thinking-resumption-2026-07-17", "prompt-caching-evict-2026-05-12",
	}
	const unknown = "unknown-feature-2099-01-01"
	for _, mode := range []string{"messages", "stream", "count_tokens"} {
		for _, native := range []bool{false, true} {
			for _, source := range []string{"header", "body"} {
				t.Run(mode+"/"+source+"/native="+map[bool]string{true: "true", false: "false"}[native], func(t *testing.T) {
					req := newClaudeHeaderTestRequest(t, nil)
					if mode == "count_tokens" {
						req.URL.Path += "/count_tokens"
					}
					incoming := http.Header{"Anthropic-Beta": {claudeCodeBeta}}
					var extra []string
					if source == "header" {
						incoming.Add("Anthropic-Beta", strings.Join(betas, ","))
					} else {
						extra = append(append([]string(nil), betas...), unknown)
					}
					if err := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, mode == "stream", extra, []byte(`{"model":"claude-sonnet-4-6"}`), nil, incoming, native); err != nil {
						t.Fatal(err)
					}
					counts := map[string]int{}
					for _, beta := range strings.Split(req.Header.Get("Anthropic-Beta"), ",") {
						counts[beta]++
					}
					for _, beta := range betas {
						if counts[beta] != 1 {
							t.Errorf("%s count=%d, want 1", beta, counts[beta])
						}
					}
					if counts[unknown] != 0 {
						t.Error("unrecognized lifted body beta leaked")
					}
				})
			}
		}
	}
}

func TestClaude280FeatureBetaOrder(t *testing.T) {
	body := []byte(`{"model":"claude-opus-5-5","safeguards":[{}],"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"omit"}},"messages":[{"role":"system","clear_at":"next_user_message","content":[{"type":"tool_addition","tool":{"definition":{"name":"bash"}}}]},{"role":"user","content":"x","output_config":{"timing":{"now":"2026-09-23T00:00:00Z"}}}],"cache_control":{"type":"ephemeral","evict_on_complete":true}}`)
	got := claudeCodeCLIBetas(body, map[string]bool{"thinking-resumption-2026-07-17": true}, false)
	want := "mid-conversation-system-2026-04-07,per-turn-control-2026-07-01,timing-2026-09-09,mid-conversation-tool-changes-2026-07-01,inline-tools-2026-09-15,mid-conversation-system-clear-at-2026-08-21,dangerous-tool-use-2026-09-03,effort-2025-11-24,thinking-binding-controls-2026-08-01,thinking-resumption-2026-07-17,prompt-caching-evict-2026-05-12"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("feature beta order = %q, want suffix %q", got, want)
	}
}

func TestClaude280OptionalBetasRemainGated(t *testing.T) {
	for _, model := range []string{"claude-fable-5-1", "claude-opus-5-5", "vendor/CLAUDE-OPUS-5-5", "claude-sonnet-4-6"} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"evict_on_complete"}],"tools":[{"name":"schema","input_schema":{"properties":{"evict_on_complete":{"type":"boolean"}}}}]}`)
			got := claudeCodeCLIBetas(body, nil, false)
			for _, beta := range []string{"timing-2026-09-09", "inline-tools-2026-09-15", "mid-conversation-system-clear-at-2026-08-21", "dangerous-tool-use-2026-09-03", "thinking-binding-controls-2026-08-01", "thinking-resumption-2026-07-17", "prompt-caching-evict-2026-05-12"} {
				if strings.Contains(got, beta) {
					t.Errorf("unrequested beta %q in %q", beta, got)
				}
			}
			wantPerTurn := model != "claude-sonnet-4-6"
			if strings.Contains(got, "per-turn-control-2026-07-01") != wantPerTurn {
				t.Errorf("per-turn gate: %q", got)
			}
			if strings.Contains(got, "mid-conversation-tool-changes-2026-07-01") != wantPerTurn {
				t.Errorf("unexpected 2.1.280 model beta policy: %q", got)
			}
		})
	}
}
