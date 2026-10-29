package executor

import (
	"bytes"
	"context"
	"io"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexOutboundToolSearchIDsPreserveHistory(t *testing.T) {
	for _, transport := range []string{"http", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5-codex","prompt_cache_key":"session-original","input":[{"type":"reasoning","id":"rs_original","encrypted_content":"opaque-signed-content"},{"type":"tool_search_call","id":"ts_search","call_id":"search","arguments":"{}"},{"type":"tool_search_output","call_id":"search","tools":[]},{"type":"tool_search_call","id":"tsc_search","call_id":"other","arguments":"{}"}]}`)
			original := bytes.Clone(body)
			var sent []byte
			if transport == "websocket" {
				sent = buildCodexWebsocketRequestBody(body)
			} else {
				e := &CodexExecutor{}
				req, _, _, err := e.cacheHelper(context.Background(), sdktranslator.FormatOpenAIResponse, "https://example.invalid/responses", nil, cliproxyexecutor.Request{Payload: body}, body, body)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if errClose := req.Body.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				sent, err = io.ReadAll(req.Body)
				if err != nil {
					t.Fatal(err)
				}
				if req.Header.Get("Session-Id") != "session-original" {
					t.Fatal("session identity changed")
				}
			}
			id := gjson.GetBytes(sent, "input.1.id").String()
			if len(id) < 4 || id[:4] != "tsc_" || id == gjson.GetBytes(sent, "input.3.id").String() {
				t.Fatalf("legacy ID not normalized without collision: %s", sent)
			}
			for path, want := range map[string]string{"input.0.id": "rs_original", "input.0.encrypted_content": "opaque-signed-content", "input.1.call_id": "search", "input.2.call_id": "search", "input.3.id": "tsc_search", "input.3.call_id": "other", "prompt_cache_key": "session-original"} {
				if got := gjson.GetBytes(sent, path).String(); got != want {
					t.Errorf("%s = %q, want %q", path, got, want)
				}
			}
			if !bytes.Equal(body, original) {
				t.Fatal("saved input history mutated")
			}
		})
	}
}
