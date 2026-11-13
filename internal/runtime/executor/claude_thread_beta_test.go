package executor

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/sjson"
)

// Thread capabilities are declared independently of the current turn body.
func TestClaudeThreadFirstTurnKeepsDeclaredCapabilities(t *testing.T) {
	const betas = "message-threads-2026-08-12,effort-2025-11-24,thinking-display-updates-2026-08-18,extended-cache-ttl-2025-04-11"
	for _, confirmed := range []bool{false, true} {
		name := "reconstructed"
		if confirmed {
			name = "confirmed"
		}
		t.Run(name, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5","thread":{"type":"create"},"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`)
			req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", nil)
			if err != nil {
				t.Fatal(err)
			}
			incoming := http.Header{"Anthropic-Beta": []string{betas}}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-thread-declarations"}}
			if err := applyClaudeHeaders(req, auth, "key-thread-declarations", false, nil, body, nil, incoming, confirmed); err != nil {
				t.Fatal(err)
			}
			if got := req.Header.Get("Anthropic-Beta"); got != betas {
				t.Fatalf("first-turn betas = %q, want declared Thread capabilities %q", got, betas)
			}
		})
	}
}

func TestClaudeThreadCapabilityGatingScope(t *testing.T) {
	for _, endpoint := range []string{"messages", "count_tokens"} {
		for _, thread := range []bool{false, true} {
			for _, bodyLifted := range []bool{false, true} {
				name := fmt.Sprintf("%s/thread=%t/bodyLifted=%t", endpoint, thread, bodyLifted)
				t.Run(name, func(t *testing.T) {
					body := []byte(`{"model":"claude-opus-5","thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hello"}]}`)
					if thread {
						body, _ = sjson.SetBytes(body, "thread", map[string]string{"type": "create"})
					}
					incoming := http.Header{}
					var extra []string
					if bodyLifted {
						extra = []string{claudeEffortBeta, claudeThinkingDisplayUpdatesBeta}
					} else {
						incoming.Set("Anthropic-Beta", claudeEffortBeta+","+claudeThinkingDisplayUpdatesBeta)
					}
					req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/"+endpoint, nil)
					if err != nil {
						t.Fatal(err)
					}
					if err := applyClaudeHeaders(req, nil, "key", false, extra, body, nil, incoming, false); err != nil {
						t.Fatal(err)
					}
					want := thread && endpoint == "messages"
					for _, beta := range []string{claudeEffortBeta, claudeThinkingDisplayUpdatesBeta} {
						if got := strings.Contains(req.Header.Get("Anthropic-Beta"), beta); got != want {
							t.Errorf("%s present=%t, want %t", beta, got, want)
						}
					}
				})
			}
		}
	}
}
