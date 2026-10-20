package helps

import (
	"testing"
)

func TestSyncOctober21SDKTSKeepsStrongSignalBoundary(t *testing.T) {
	body := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
	headers := confirmedClaudeCodeHeaders()
	headers.Set("User-Agent", "claude-cli/2.1.304 (external, sdk-ts, agent-sdk/0.3.276)")
	if !DetectClaudeCodeRequest(headers, body, false).Confirmed {
		t.Fatal("standard sdk-ts not recognized")
	}
	for _, missing := range []string{"X-App", "User-Agent", "Anthropic-Beta"} {
		h := headers.Clone()
		h.Del(missing)
		if DetectClaudeCodeRequest(h, body, false).Confirmed {
			t.Fatalf("confirmed without %s", missing)
		}
	}
	if DetectClaudeCodeRequest(headers, []byte(`{"metadata":{"user_id":"invalid"}}`), false).Confirmed {
		t.Fatal("confirmed invalid metadata")
	}
	if !DetectClaudeCodeRequest(headers, []byte(`{"messages":[]}`), true).Confirmed {
		t.Fatal("count_tokens incorrectly requires metadata")
	}
	headers.Set("User-Agent", "claude-cli/2.1.304 (external, sdk-py, agent-sdk/0.3.276)")
	if DetectClaudeCodeRequest(headers, body, false).Confirmed {
		t.Fatal("sdk-py unexpectedly enabled")
	}
}
