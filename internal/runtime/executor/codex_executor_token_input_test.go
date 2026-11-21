package executor

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCountTokensAcceptsTextShorthands(t *testing.T) {
	const explicit = `[{"type":"message","role":"user","content":[{"type":"input_text","text":"Count all of these words."}]}]`
	executor := NewCodexExecutor(&config.Config{})
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}
	countInput := func(t *testing.T, input string) int64 {
		t.Helper()
		resp, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
			Model:   "gpt-5.4",
			Payload: []byte(`{"model":"gpt-5.4","instructions":"","input":` + input + `}`),
		}, opts)
		if err != nil {
			t.Fatal(err)
		}
		return gjson.GetBytes(resp.Payload, "response.usage.input_tokens").Int()
	}
	want := countInput(t, explicit)
	if want <= 0 {
		t.Fatalf("explicit input token count = %d, want positive", want)
	}

	for _, tc := range []struct{ name, input string }{
		{"plain input", `"Count all of these words."`},
		{"explicit message with plain content", `[{"type":"message","role":"user","content":"Count all of these words."}]`},
		{"implicit message with plain content", `[{"role":"user","content":"Count all of these words."}]`},
		{"implicit message with content parts", `[{"role":"user","content":[{"type":"input_text","text":"Count all of these words."}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := countInput(t, tc.input); got != want {
				t.Fatalf("input token count = %d, want explicit message count %d", got, want)
			}
		})
	}
}
