package translator

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestRegistryTranslateRequestAppliesSupportedSummaryIntent(t *testing.T) {
	tests := []struct {
		name       string
		from       Format
		input      string
		translated string
		want       string
		wantExists bool
	}{
		{name: "chat effort", from: FormatOpenAI, input: `{"reasoning_effort":"high"}`, translated: `{"thinking":{"type":"adaptive"}}`, want: "summarized", wantExists: true},
		{name: "responses effort only", from: FormatOpenAIResponse, input: `{"reasoning":{"effort":"high"}}`, translated: `{"thinking":{"type":"adaptive"}}`},
		{name: "responses summary", from: FormatOpenAIResponse, input: `{"reasoning":{"effort":"high","summary":"auto"}}`, translated: `{"thinking":{"type":"adaptive"}}`, want: "summarized", wantExists: true},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			registry := NewRegistry()
			registry.Register(testCase.from, FormatClaude, func(_ string, _ []byte, _ bool) []byte {
				return []byte(testCase.translated)
			}, ResponseTransform{})
			out := registry.TranslateRequest(testCase.from, FormatClaude, "claude-opus-5", []byte(testCase.input), false)
			result := gjson.GetBytes(out, "thinking.display")
			if result.Exists() != testCase.wantExists || (testCase.wantExists && result.String() != testCase.want) {
				t.Fatalf("thinking.display = %q exists=%v; body=%s", result.String(), result.Exists(), out)
			}
		})
	}
}

func TestRegistryTranslateRequestDoesNotMixSummaryIntoFallback(t *testing.T) {
	registry := NewRegistry()
	body := []byte(`{"model":"gpt-5.4","reasoning":{"summary":"auto"},"input":"hi"}`)
	out := registry.TranslateRequest(FormatOpenAIResponse, Format("unsupported"), "gpt-5.4", body, false)
	if !bytes.Equal(out, body) {
		t.Fatalf("missing translator changed fallback body: got %s, want %s", out, body)
	}
}

func TestRegistryTranslateRequestAppliesSummaryAfterPluginTranslation(t *testing.T) {
	registry := NewRegistry()
	registry.SetPluginHooks(&fakePluginHooks{
		requestTranslateBody: []byte(`{"thinking":{"type":"adaptive"}}`),
		requestTranslateOK:   true,
	})
	out := registry.TranslateRequest(FormatOpenAIResponse, FormatClaude, "claude-opus-5", []byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`), false)
	if got := gjson.GetBytes(out, "thinking.display").String(); got != "summarized" {
		t.Fatalf("plugin-translated summary = %q, want summarized; body=%s", got, out)
	}
}

func TestRegistryTranslateRequestNormalizerOwnsFinalSummaryField(t *testing.T) {
	registry := NewRegistry()
	registry.Register(FormatOpenAIResponse, FormatClaude, func(_ string, _ []byte, _ bool) []byte {
		return []byte(`{"thinking":{"type":"adaptive"}}`)
	}, ResponseTransform{})
	registry.SetPluginHooks(&fakePluginHooks{normalizeRequest: func(body []byte) []byte {
		out, _ := sjson.DeleteBytes(body, "thinking.display")
		return out
	}})
	out := registry.TranslateRequest(FormatOpenAIResponse, FormatClaude, "claude-opus-5", []byte(`{"reasoning":{"summary":"auto"},"input":"hi"}`), false)
	if gjson.GetBytes(out, "thinking.display").Exists() {
		t.Fatalf("summary post-processing overrode request normalizer: %s", out)
	}
}
