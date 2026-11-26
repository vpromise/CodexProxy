package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// thinkingSignatureFixtures are signature shapes that survive a JSON round trip
// only when every stage performs targeted edits instead of re-encoding the body.
// They cover base64 padding, JSON metacharacters, escape sequences, astral-plane
// runes and an oversized value.
func thinkingSignatureFixtures() []string {
	return []string{
		"ErUBCkYIBRgCKkDq+9zN/vQ7aB1c2dEf==",
		`sig/with+slashes==and"quotes"and\backslashes`,
		"line\nbreak\ttab\u0000null\u001fcontrol",
		"unicode-\u4e2d\u6587-\U0001f600-\u200b-\ufeff",
		"a/b<c>d&e'f\u2028\u2029",
		strings.Repeat("EqQBCkYIBRgCKkD", 400) + "==",
	}
}

// collectThinkingSignatures returns every messages[].content[].signature value in
// document order.
func collectThinkingSignatures(t *testing.T, body []byte) []string {
	t.Helper()
	var found []string
	gjson.GetBytes(body, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			if signature := block.Get("signature"); signature.Exists() {
				found = append(found, signature.String())
			}
			return true
		})
		return true
	})
	return found
}

// buildThinkingHistoryPayload renders a multi-turn conversation whose assistant
// turns carry thinking blocks with the supplied signatures, plus a declared tool
// so the OAuth MCP alias pass has real work to do.
func buildThinkingHistoryPayload(t *testing.T, signatures []string, firstUserText string) []byte {
	t.Helper()
	type block map[string]any
	messages := []any{
		map[string]any{"role": "user", "content": []any{block{"type": "text", "text": firstUserText}}},
	}
	for i, signature := range signatures {
		messages = append(messages, map[string]any{
			"role": "assistant",
			"content": []any{
				block{"type": "thinking", "thinking": "reasoning step", "signature": signature},
				block{"type": "tool_use", "id": "toolu_" + string(rune('a'+i)), "name": "search_web", "input": map[string]any{}},
			},
		})
		messages = append(messages, map[string]any{
			"role": "user",
			"content": []any{
				block{"type": "tool_result", "tool_use_id": "toolu_" + string(rune('a'+i)), "content": "tool output"},
			},
		})
	}
	payload := map[string]any{
		"model":      "claude-opus-5",
		"max_tokens": 1024,
		"thinking":   map[string]any{"type": "adaptive"},
		"messages":   messages,
		"tools": []any{
			map[string]any{"name": "search_web", "input_schema": map[string]any{"type": "object"}},
		},
	}
	encoded, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		t.Fatalf("marshal fixture payload: %v", errMarshal)
	}
	return encoded
}

func TestClaudeFable52NativeSignedContinuation(t *testing.T) {
	const model = "claude-fable-5-2"
	const signature = "ErUBCkYIBRgCKkDq+9zN/vQ7aB1c2dEf=="
	body, err := sjson.SetBytes(buildThinkingHistoryPayload(t, []string{signature}, "question"), "model", model)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range []string{"", "error"} {
		t.Run("binding="+binding, func(t *testing.T) {
			request := body
			if binding != "" {
				var errSet error
				request, errSet = sjson.SetBytes(body, "thinking.block_binding.prefix_mismatch_behavior", binding)
				if errSet != nil {
					t.Fatal(errSet)
				}
			}
			for _, stream := range []bool{false, true} {
				transport := roundTripperFunc(func(out *http.Request) (*http.Response, error) {
					wire, errRead := io.ReadAll(out.Body)
					if errRead != nil {
						t.Fatal(errRead)
					}
					for _, path := range []string{"messages", "tools", "max_tokens"} {
						if got, want := gjson.GetBytes(wire, path).Raw, gjson.GetBytes(request, path).Raw; got != want {
							t.Errorf("signed request %s = %s, want %s", path, got, want)
						}
					}
					if got := gjson.GetBytes(wire, "thinking.block_binding.prefix_mismatch_behavior").String(); got != binding {
						t.Errorf("block binding = %q, want %q", got, binding)
					}
					if binding != "" && !strings.Contains(helps.HeaderValueCaseInsensitive(out.Header, "Anthropic-Beta"), claudeThinkingBindingBeta) {
						t.Error("explicit binding is missing its Anthropic-Beta")
					}
					contentType := "application/json"
					resp := `{"id":"msg_fixture","type":"message","role":"assistant","model":"claude-fable-5-2","content":[{"type":"thinking","thinking":"continued","signature":"EgI="}],"stop_reason":"max_tokens","usage":{"input_tokens":8,"output_tokens":1024},"input_transformations":[]}`
					if stream {
						contentType = "text/event-stream"
						resp = claudeReplayThinkingStream()
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(resp)), Request: out}, nil
				})
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
				executor := NewClaudeExecutor(&config.Config{DisableClaudeCloakMode: true})
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fixture-native-fable"}}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: stream}
				req := cliproxyexecutor.Request{Model: model, Payload: request}
				if stream {
					req.Payload = helps.SetBoolIfDifferent(request, "stream", true)
					out, errStream := executor.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					var events strings.Builder
					for chunk := range out.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
						events.Write(chunk.Payload)
					}
					if !strings.Contains(events.String(), `"type":"signature_delta","signature":"EgI="`) {
						t.Errorf("native stream lost the signature event: %s", events.String())
					}
					continue
				}
				out, errExecute := executor.Execute(ctx, auth, req, opts)
				if errExecute != nil {
					t.Fatal(errExecute)
				}
				if got := gjson.GetManyBytes(out.Payload, "content.0.signature", "stop_reason", "input_transformations"); got[0].String() != "EgI=" || got[1].String() != "max_tokens" || !got[2].IsArray() {
					t.Errorf("native response lost the signature, stop reason or transformations: %s", out.Payload)
				}
			}
		})
	}
}

// TestClaudeThinkingSignaturesSurviveUpstreamPreparation pins the roadmap
// requirement that thinking-block signatures replay byte-for-byte through the
// upstream request pipeline: cloaking (system blocks, currentDate, CCH signing)
// followed by the OAuth MCP tool alias pass.
func TestClaudeThinkingSignaturesSurviveUpstreamPreparation(t *testing.T) {
	signatures := thinkingSignatureFixtures()
	payload := buildThinkingHistoryPayload(t, signatures, "first question")

	if got := collectThinkingSignatures(t, payload); len(got) != len(signatures) {
		t.Fatalf("fixture built %d signatures, want %d", len(got), len(signatures))
	}

	cfg := &config.Config{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"cloak_mode": "always"}}

	cloaked, didCloak, errCloaking := applyCloaking(
		context.Background(),
		cfg,
		auth,
		payload,
		"sk-ant-oat-test",
		false,
		true,
	)
	if errCloaking != nil {
		t.Fatalf("applyCloaking() error = %v", errCloaking)
	}
	if !didCloak {
		t.Fatal("applyCloaking() cloaked = false, want true")
	}

	prepared, reverseMap := prepareClaudeOAuthToolNamesForUpstream(cloaked, claudeMCPAliasOptions{secret: "signature-fixture-caller"})
	if len(reverseMap) == 0 {
		t.Fatal("expected the MCP alias pass to rewrite the declared tool")
	}

	for stage, body := range map[string][]byte{"cloaked": cloaked, "prepared": prepared} {
		got := collectThinkingSignatures(t, body)
		if len(got) != len(signatures) {
			t.Fatalf("%s stage produced %d signatures, want %d", stage, len(got), len(signatures))
		}
		for i, want := range signatures {
			if got[i] != want {
				t.Fatalf("%s stage signature[%d] mutated:\n got  %q\n want %q", stage, i, got[i], want)
			}
		}
	}
}

// TestClaudeThinkingSignaturesSurviveSensitiveWordObfuscation guards the case
// where cloaking rewrites message text: obfuscation must never reach into an
// opaque thinking signature, even when the signature contains the trigger word.
func TestClaudeThinkingSignaturesSurviveSensitiveWordObfuscation(t *testing.T) {
	const sensitive = "proxy"
	signature := "ErUBCkYIBRgC" + sensitive + "KkDq+9zN=="
	// The visible user text carries the same trigger word, so the assertions below
	// prove obfuscation ran and still left the signature untouched.
	payload := buildThinkingHistoryPayload(t, []string{signature}, "please use the "+sensitive+" now")

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{
			APIKey: "key-123",
			Cloak:  &config.CloakConfig{SensitiveWords: []string{sensitive}},
		}},
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123"}}

	out, didCloak, errCloaking := applyCloaking(context.Background(), cfg, auth, payload, "key-123", false, true)
	if errCloaking != nil {
		t.Fatalf("applyCloaking() error = %v", errCloaking)
	}
	if !didCloak {
		t.Fatal("applyCloaking() cloaked = false, want true")
	}

	var obfuscatedUserText bool
	gjson.GetBytes(out, "messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, contentBlock gjson.Result) bool {
			if contentBlock.Get("type").String() != "text" {
				return true
			}
			if text := contentBlock.Get("text").String(); strings.Contains(text, "\u200B") {
				obfuscatedUserText = true
				return false
			}
			return true
		})
		return !obfuscatedUserText
	})
	if !obfuscatedUserText {
		t.Fatal("sensitive word obfuscation never ran, so the signature assertion would be vacuous")
	}

	got := collectThinkingSignatures(t, out)
	if len(got) != 1 {
		t.Fatalf("collected %d signatures, want 1", len(got))
	}
	if got[0] != signature {
		t.Fatalf("signature mutated by obfuscation:\n got  %q\n want %q", got[0], signature)
	}
}
