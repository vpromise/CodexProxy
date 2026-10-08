package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutorSystemPlacementByCredential(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, shape := range []struct {
			name, messages  string
			count, insertAt int64
		}{
			{"single", `[{"role":"user","content":"request"}]`, 1, 1},
			{"leading", `[{"role":"user","content":"prompt"},{"role":"user","content":"context"},{"role":"assistant","content":"answer"},{"role":"user","content":"follow-up"}]`, 4, 2},
			{"trailing", `[{"role":"user","content":"first"},{"role":"user","content":"second"}]`, 2, 2},
		} {
			for _, mode := range []string{"execute", "stream", "count_tokens"} {
				t.Run(fmt.Sprintf("oauth=%t/%s/%s", oauth, shape.name, mode), func(t *testing.T) {
					key := "key-test"
					if oauth {
						key = "sk-ant-oat-system-test"
					}
					auth := &cliproxyauth.Auth{
						ID:         "system-policy-fixture",
						Attributes: map[string]string{"api_key": key, "cloak_mode": "always"},
						Metadata:   claudeOAuthTestMetadata(),
					}
					ex := NewClaudeExecutor(&config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: key}}})
					upstream := &midSystemUpstream{}
					payload := []byte(fmt.Sprintf(`{"model":"claude-opus-5","max_tokens":32,"system":"caller guidance","messages":%s,"stream":%t}`, shape.messages, mode == "stream"))
					req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Stream: mode == "stream"}
					ctx := upstream.context(t, nil)
					switch mode {
					case "execute":
						if _, err := ex.Execute(ctx, auth, req, opts); err != nil {
							t.Fatal(err)
						}
					case "stream":
						result, err := ex.ExecuteStream(ctx, auth, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count_tokens":
						if _, err := ex.countTokensUpstream(ctx, auth, req, opts); err != nil {
							t.Fatal(err)
						}
					}
					if !upstream.called {
						t.Fatal("expected mocked upstream request")
					}
					top := gjson.GetBytes(upstream.body, "system")
					messages := gjson.GetBytes(upstream.body, "messages")
					if oauth {
						if strings.Contains(top.Raw, "caller guidance") {
							t.Fatalf("OAuth caller instructions remained top-level: %s", upstream.body)
						}
						if mode == "count_tokens" && top.Exists() {
							t.Fatalf("count_tokens retained system: %s", upstream.body)
						}
						if got := messages.Get("#").Int(); got != shape.count+1 {
							t.Fatalf("messages = %d, want %d: %s", got, shape.count+1, upstream.body)
						}
						relocated := messages.Get(fmt.Sprintf("%d", shape.insertAt))
						if relocated.Get("role").String() != "system" || relocated.Get("content.0.text").String() != "caller guidance" {
							t.Fatalf("missing relocated instructions: %s", upstream.body)
						}
					} else {
						if !strings.Contains(top.Raw, "caller guidance") {
							t.Fatalf("API-key caller instructions missing from top-level: %s", upstream.body)
						}
						if got := messages.Get("#").Int(); got != shape.count {
							t.Fatalf("API-key message count = %d, want %d", got, shape.count)
						}
					}
				})
			}
		}
	}
}
