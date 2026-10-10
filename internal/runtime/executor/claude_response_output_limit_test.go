package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutorResponsesDefaultOutputLimit(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ctx := upstream.context(t, nil)
			ex := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: "output-limit-fixture", Attributes: map[string]string{"api_key": "key-fixture", "cloak_mode": "never"}}
			payload := []byte(`{"model":"claude-opus-5-5","input":"hello"}`)
			req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload}
			if mode == "execute" {
				if _, err := ex.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
			} else {
				result, err := ex.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			}
			if !upstream.called || gjson.GetBytes(upstream.body, "max_tokens").Int() != 128000 {
				t.Fatalf("upstream called=%t max_tokens=%s, want 128000", upstream.called, gjson.GetBytes(upstream.body, "max_tokens").Raw)
			}
		})
	}
}
