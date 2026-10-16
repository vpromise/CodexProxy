package executor

import (
	"bytes"
	"fmt"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeExplicitBindingBetaAcrossProfiles(t *testing.T) {
	for _, mode := range []string{"messages", "stream", "count_tokens"} {
		for _, profile := range []string{"caller", "native", "cloaked"} {
			for _, binding := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/binding=%v", mode, profile, binding), func(t *testing.T) {
					req := newClaudeHeaderTestRequest(t, nil)
					if mode == "count_tokens" {
						req.URL.Path += "/count_tokens"
					}
					auth, key := claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey
					if profile == "caller" {
						auth = &cliproxyauth.Auth{ID: "api-fixture", Attributes: map[string]string{"api_key": "sk-ant-api-fixture"}}
						key = "sk-ant-api-fixture"
					}
					body := []byte(`{"model":"claude-opus-5-5","thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`)
					if binding {
						body = bytes.Replace(body, []byte(`"type":"adaptive"`), []byte(`"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"error"}`), 1)
					}
					original := bytes.Clone(body)
					incoming := http.Header{"Anthropic-Beta": {claudeCodeBeta}}
					if err := applyClaudeHeaders(req, auth, key, mode == "stream", nil, body, nil, incoming, profile == "native"); err != nil {
						t.Fatal(err)
					}
					n := 0
					for _, beta := range strings.Split(req.Header.Get("Anthropic-Beta"), ",") {
						if beta == claudeThinkingBindingBeta {
							n++
						}
					}
					want := 0
					if binding {
						want = 1
					}
					if n != want {
						t.Fatalf("binding beta count=%d want=%d header=%s", n, want, req.Header.Get("Anthropic-Beta"))
					}
					if !bytes.Equal(original, body) {
						t.Fatal("header policy rewrote body")
					}
				})
			}
		}
	}
}
