package helps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCheckedTranslationPreservesEachUserTurn(t *testing.T) {
	for _, route := range []struct {
		name      string
		from, to  sdktranslator.Format
		bad, good string
	}{
		{"chat-claude", sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, `{"type":"file","file":{"file_id":"opaque"}}`, `{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0="}}`},
		{"responses-claude", sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, `{"type":"input_audio","data":"YQ==","format":"wav"}`, `{"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0="}`},
		{"claude-chat", sdktranslator.FormatClaude, sdktranslator.FormatOpenAI, `{"type":"container_upload","file_id":"opaque"}`, `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}`},
		{"claude-codex", sdktranslator.FormatClaude, sdktranslator.FormatCodex, `{"type":"image","source":{"type":"file","file_id":"opaque"}}`, `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}`},
		{"chat-codex", sdktranslator.FormatOpenAI, sdktranslator.FormatCodex, `{"type":"file","file":{"file_id":"opaque"}}`, `{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBERi0="}}`},
		{"responses-chat", sdktranslator.FormatOpenAIResponse, sdktranslator.FormatOpenAI, `{"type":"input_file","file_url":"https://fixture.invalid/file.pdf"}`, `{"type":"input_file","file_data":"data:application/pdf;base64,JVBERi0="}`},
	} {
		for _, compat := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				for _, scenario := range []string{"alone", "blank", "system", "other-turns", "mixed", "valid", "assistant"} {
					t.Run(fmt.Sprintf("%s/%s/compat=%t/stream=%t", route.name, scenario, compat, stream), func(t *testing.T) {
						textType := "text"
						key := "messages"
						if route.from == sdktranslator.FormatOpenAIResponse {
							textType = "input_text"
							key = "input"
						}
						content := []any{json.RawMessage(route.bad)}
						wantErr := true
						role := "user"
						switch scenario {
						case "blank":
							content = append(content, map[string]any{"type": textType, "text": " \t "})
						case "mixed":
							content = append(content, map[string]any{"type": textType, "text": "surviving text"})
							wantErr = false
						case "valid":
							content = []any{json.RawMessage(route.good)}
							wantErr = false
						case "assistant":
							role = "assistant"
							wantErr = false
						}
						messages := []any{map[string]any{"role": role, "content": content}}
						if scenario == "system" {
							messages = append([]any{map[string]any{"role": "system", "content": "instructions"}}, messages...)
						}
						if scenario == "other-turns" {
							messages = append([]any{map[string]any{"role": "user", "content": "before"}}, messages...)
							messages = append(messages, map[string]any{"role": "user", "content": "after"})
						}
						payload, err := json.Marshal(map[string]any{"model": "claude-opus-5", key: messages})
						if err != nil {
							t.Fatal(err)
						}
						body, err := TranslateRequestWithAPIKeyModelCompatibilityChecked(t.Context(), nil, &config.Config{}, route.from, route.to, "claude-opus-5", payload, stream, compat)
						if (err != nil) != wantErr {
							t.Fatalf("error=%v wantError=%t body=%s", err, wantErr, body)
						}
						if wantErr {
							var status interface{ StatusCode() int }
							if !errors.As(err, &status) || status.StatusCode() != 400 {
								t.Fatalf("unexpected error: %v", err)
							}
						}
						if scenario == "mixed" && !bytes.Contains(body, []byte("surviving text")) {
							t.Fatal("valid text was lost")
						}
						if scenario == "valid" && !bytes.Contains(body, []byte("JVBERi0=")) {
							t.Fatal("valid attachment was lost")
						}
					})
				}
			}
		}
	}
}

func TestCheckedRequestPairUsesWorkingError(t *testing.T) {
	bad := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"opaque"}}]}]}`)
	good := []byte(`{"messages":[{"role":"user","content":"repaired"}]}`)
	for _, compat := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, tc := range []struct {
				name              string
				original, working []byte
				fail              bool
			}{{"both bad", bad, bad, true}, {"bad working", good, bad, true}, {"repaired working", bad, good, false}, {"same good", good, good, false}} {
				t.Run(fmt.Sprintf("%s/%t/%t", tc.name, compat, stream), func(t *testing.T) {
					original, working, err := TranslateRequestPairWithAPIKeyModelCompatibilityChecked(t.Context(), nil, nil, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "claude-opus-5", tc.original, tc.working, stream, compat)
					if (err != nil) != tc.fail {
						t.Fatalf("error=%v wantError=%t", err, tc.fail)
					}
					snapshot := bytes.Clone(original)
					if len(working) > 0 {
						working[0] = '!'
					}
					if !bytes.Equal(original, snapshot) {
						t.Fatal("working buffer aliases baseline")
					}
				})
			}
		}
	}
	hooks := &pairRequestPluginHooks{}
	sdktranslator.SetPluginHooks(hooks)
	t.Cleanup(func() { sdktranslator.SetPluginHooks(nil) })
	a, b, err := TranslateRequestPairWithCodexMultiAgentV2Checked(t.Context(), nil, nil, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "claude-opus-5", good, good, false)
	if err != nil || hooks.calls != 2 || gjson.GetBytes(a, "plugin_call").Int() != 1 || gjson.GetBytes(b, "plugin_call").Int() != 2 {
		t.Fatalf("stateful hooks changed: calls=%d error=%v", hooks.calls, err)
	}
}

func TestCheckedTranslationKeepsNativeAndSystemOnlyInputs(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"container_upload","file_id":"opaque"}]}]}`)
	native, err := TranslateRequestWithAPIKeyModelCompatibilityChecked(t.Context(), nil, nil, sdktranslator.FormatClaude, sdktranslator.FormatClaude, "native", body, false, false)
	if err != nil || !strings.Contains(string(native), "opaque") {
		t.Fatalf("native payload changed: %s %v", native, err)
	}
	_, err = TranslateRequestWithAPIKeyModelCompatibilityChecked(t.Context(), nil, nil, sdktranslator.FormatOpenAI, sdktranslator.FormatClaude, "claude-opus-5", []byte(`{"messages":[{"role":"system","content":"instructions"}]}`), false, false)
	if err != nil {
		t.Fatal(err)
	}
}
