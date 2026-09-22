package helps

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCompatibilityRequestPairPreservesClaudeTranslation(t *testing.T) {
	const model = "claude-sonnet-4-5"
	ctx := context.Background()
	cfg := &config.Config{}
	cases := []struct {
		from    sdktranslator.Format
		payload []byte
	}{
		{sdktranslator.FormatOpenAIResponse, codexToolHistoryPayload(2)},
		{sdktranslator.FormatOpenAI, []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"result","cache_control":{"type":"ephemeral","ttl":"1h"}}]}],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`)},
		{sdktranslator.FormatClaude, []byte(`{"model":"claude-sonnet-4-5","system":[{"type":"text","text":"system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hello"}],"thinking":{"type":"disabled"}}`)},
	}
	for _, tc := range cases {
		for _, stream := range []bool{false, true} {
			for _, compat := range []bool{false, true} {
				for _, identity := range []string{"same", "detached", "different"} {
					t.Run(fmt.Sprintf("%s/stream=%t/compat=%t/%s", tc.from, stream, compat, identity), func(t *testing.T) {
						original := bytes.Clone(tc.payload)
						request := original
						if identity == "detached" {
							request = bytes.Clone(original)
							request = request[:len(request):len(request)]
						} else if identity == "different" {
							request = []byte(`{"model":"claude-sonnet-4-5","input":"changed","messages":[{"role":"user","content":"changed"}]}`)
						}
						wantBase := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, tc.from, sdktranslator.FormatClaude, model, original, stream, compat)
						wantWork := TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, tc.from, sdktranslator.FormatClaude, model, request, stream, compat)
						base, work := TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, tc.from, sdktranslator.FormatClaude, model, original, request, stream, compat)
						if !bytes.Equal(base, wantBase) || !bytes.Equal(work, wantWork) {
							t.Fatal("paired translation differs from the two standalone translations")
						}
						if len(base) == 0 || len(work) == 0 {
							t.Fatal("translation unexpectedly returned an empty payload")
						}
						baseSnapshot := bytes.Clone(base)
						work[0] = '!'
						if !bytes.Equal(base, baseSnapshot) || !bytes.Equal(original, tc.payload) {
							t.Fatal("working mutation changed the baseline or original request")
						}
					})
				}
			}
		}
	}
}

func TestCompatibilityRequestPairPreservesHooks(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, compat := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%t/compat=%t", stream, compat), func(t *testing.T) {
				hooks := &pairRequestPluginHooks{}
				sdktranslator.SetPluginHooks(hooks)
				t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
				request := []byte(`{"model":"test","messages":[{"role":"user","content":"hello"}]}`)
				base, work := TranslateRequestPairWithAPIKeyModelCompatibility(context.Background(), nil, &config.Config{}, sdktranslator.FormatClaude, sdktranslator.FormatClaude, "test", request, request, stream, compat)
				if hooks.calls != 2 || gjson.GetBytes(base, "plugin_call").Int() != 1 || gjson.GetBytes(work, "plugin_call").Int() != 2 {
					t.Fatalf("stateful plugin calls lost their order or independence: calls=%d", hooks.calls)
				}
			})
		}
	}
}

func BenchmarkClaudeCompatibilityRequestPair(b *testing.B) {
	const model = "claude-sonnet-4-5"
	payload := []byte(`{"model":"claude-sonnet-4-5","input":[{"role":"user","content":"` + strings.Repeat("x", 1<<20) + `"}]}`)
	ctx := context.Background()
	cfg := &config.Config{}
	from, to := sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude
	for _, compat := range []bool{false, true} {
		for _, paired := range []bool{false, true} {
			b.Run(fmt.Sprintf("compat=%t/paired=%t", compat, paired), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(payload)))
				for b.Loop() {
					if paired {
						TranslateRequestPairWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, payload, true, compat)
					} else {
						TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, true, compat)
						TranslateRequestWithAPIKeyModelCompatibility(ctx, nil, cfg, from, to, model, payload, true, compat)
					}
				}
			})
		}
	}
}
