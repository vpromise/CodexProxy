package test

import (
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSummaryIntentTranslationForSupportedFormats(t *testing.T) {
	tests := []struct {
		name       string
		from       sdktranslator.Format
		to         sdktranslator.Format
		body       string
		path       string
		want       string
		wantExists bool
	}{
		{name: "chat to Claude", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, path: "thinking.display", want: "summarized", wantExists: true},
		{name: "responses to Claude", from: sdktranslator.FormatOpenAIResponse, to: sdktranslator.FormatClaude, body: `{"model":"claude-opus-5","reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`, path: "thinking.display", want: "summarized", wantExists: true},
		{name: "Claude to Codex", from: sdktranslator.FormatClaude, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","max_tokens":1024,"thinking":{"type":"adaptive","display":"summarized"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary", want: "auto", wantExists: true},
		{name: "chat to Codex", from: sdktranslator.FormatOpenAI, to: sdktranslator.FormatCodex, body: `{"model":"gpt-5.4","reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`, path: "reasoning.summary", want: "auto", wantExists: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			out := sdktranslator.TranslateRequest(testCase.from, testCase.to, "", []byte(testCase.body), true)
			result := gjson.GetBytes(out, testCase.path)
			if result.Exists() != testCase.wantExists {
				t.Fatalf("%s exists = %v, want %v; body=%s", testCase.path, result.Exists(), testCase.wantExists, out)
			}
			if testCase.wantExists && result.String() != testCase.want {
				t.Fatalf("%s = %q, want %q; body=%s", testCase.path, result.String(), testCase.want, out)
			}
		})
	}
}
