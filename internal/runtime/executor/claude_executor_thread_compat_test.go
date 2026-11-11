package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Native clients repeat their complete beta contract while the server retains
// create-only tools. Cover both a caller MCP tool and a plain client tool so a
// continuation cannot accidentally enter the reconstructed aliasing path.
func TestClaudeNativeThreadPreservesBetaAndToolContract(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			headers := claudeNativeHelperHeaders(claudeCode2321MainBetas)
			headers.Set("User-Agent", "claude-cli/2.1.321 (external, cli)")
			headers.Set("X-Stainless-Package-Version", "0.128.0")
			headers.Set("X-Stainless-Runtime-Version", "v26.12.1")
			create := claudeIsolationNativeBody(claudeNativeHelperSessionID, "")
			create, _ = sjson.SetBytes(create, "thread", map[string]string{"type": "create"})
			create, _ = sjson.SetRawBytes(create, "tools", []byte(`[{"name":"mcp__docs__lookup","input_schema":{"type":"object"}},{"name":"Read","input_schema":{"type":"object"},"defer_loading":true}]`))
			create, _ = sjson.SetRawBytes(create, "tool_choice", []byte(`{"type":"auto"}`))
			continued, _ := sjson.DeleteBytes(create, "system")
			continued, _ = sjson.DeleteBytes(continued, "tools")
			continued, _ = sjson.SetBytes(continued, "thread", map[string]string{"type": "continue", "previous_message_id": "msg_audit"})
			continued, _ = sjson.SetRawBytes(continued, "messages", []byte(`[{"role":"user","content":"continue this synthetic native thread"}]`))
			createdWire := captureIsolatedClaudeRequest(t, true, mode, create, headers, sdktranslator.FormatClaude)
			continuedWire := captureIsolatedClaudeRequest(t, true, mode, continued, headers, sdktranslator.FormatClaude)
			for _, wire := range []claudeIsolationWire{createdWire, continuedWire} {
				if got := helps.HeaderValueCaseInsensitive(wire.Headers, "Anthropic-Beta"); got != claudeCode2321MainBetas {
					t.Errorf("native thread beta contract changed: %s", got)
				}
				if got := gjson.GetBytes(wire.Body, "tool_choice").Raw; got != `{"type":"auto"}` {
					t.Errorf("fixed tool_choice changed: %s", got)
				}
			}
			tools := gjson.GetBytes(createdWire.Body, "tools").Array()
			if len(tools) != 2 {
				t.Fatalf("native create tool declarations changed: %s", gjson.GetBytes(createdWire.Body, "tools").Raw)
			}
			for index, want := range []string{"mcp__docs__lookup", "Read"} {
				if got := tools[index].Get("name").String(); got != want {
					t.Errorf("native tool name changed: got %q, want %q", got, want)
				}
			}
			if gjson.GetBytes(continuedWire.Body, "system").Exists() || gjson.GetBytes(continuedWire.Body, "tools").Exists() {
				t.Fatal("native continuation acquired create-only prefix fields")
			}
		})
	}
}

func TestClaudeThreadContinuationPreservesServerOwnedPrefix(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		t.Run(mode, func(t *testing.T) {
			body := []byte(`{"model":"claude-opus-5-5","max_tokens":1024,"thread":{"type":"continue","previous_message_id":"msg_synthetic_previous"},"messages":[{"role":"user","content":"continue synthetic test"}]}`)
			wire := captureIsolatedClaudeRequest(t, true, mode, body, http.Header{}, sdktranslator.FormatClaude)
			if gjson.GetBytes(wire.Body, "system").Exists() || gjson.GetBytes(wire.Body, "tools").Exists() {
				t.Fatal("thread continuation acquired create-only fields")
			}
			if got := gjson.GetBytes(wire.Body, "thread").Raw; got != gjson.GetBytes(body, "thread").Raw {
				t.Fatalf("thread changed: %s", got)
			}
			if strings.Contains(string(wire.Body), "<system-reminder>") {
				t.Fatal("thread continuation acquired a synthetic reminder")
			}
			if !strings.Contains(helps.HeaderValueCaseInsensitive(wire.Headers, "Anthropic-Beta"), "message-threads-2026-08-12") {
				t.Fatal("thread capability beta is missing")
			}
		})
	}
}

func TestClaudeThreadInvalidRequestsAreRequestScoped(t *testing.T) {
	for _, tc := range []struct{ name, field string }{
		{"continue_system", `"system":"caller instructions",`},
		{"continue_tools", `"tools":[],`},
		{"count", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte(`{"model":"claude-opus-5","max_tokens":1024,` + tc.field + `"thread":{"type":"continue","previous_message_id":"msg_synthetic_previous"},"messages":[{"role":"user","content":"continue"}]}`)
			auth := &coreauth.Auth{ID: "thread-validation-" + tc.name, Attributes: map[string]string{"api_key": "sk-ant-oat-synthetic-thread-only"}, Metadata: claudeOAuthTestMetadata()}
			calls := 0
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("unexpected upstream dispatch")
			})))
			req := coreexecutor.Request{Model: "claude-opus-5", Payload: payload}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			e := NewClaudeExecutor(&config.Config{})
			var err error
			if tc.name == "count" {
				_, err = e.CountTokens(ctx, auth, req, opts)
			} else {
				_, err = e.Execute(ctx, auth, req, opts)
			}
			var scoped interface{ IsRequestScoped() bool }
			var status interface{ StatusCode() int }
			if !errors.As(err, &scoped) || !scoped.IsRequestScoped() || !errors.As(err, &status) || status.StatusCode() != 400 {
				t.Fatalf("expected request-scoped 400, got %v", err)
			}
			if calls != 0 {
				t.Fatalf("invalid request dispatched %d times", calls)
			}
		})
	}
}
