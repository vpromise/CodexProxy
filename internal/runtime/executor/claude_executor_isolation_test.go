package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudeIsolationWire struct {
	Headers http.Header     `json:"headers"`
	Body    json.RawMessage `json:"body"`
}

func claudeIsolationNativeBody(sessionID, parent string) []byte {
	identity := map[string]string{"device_id": strings.Repeat("f", 64), "account_uuid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "session_id": sessionID}
	if parent != "" {
		identity["parent_session_id"] = parent
	}
	id, _ := json.Marshal(identity)
	body, _ := json.Marshal(map[string]any{"model": "claude-opus-5-5", "max_tokens": 1024, "messages": []map[string]any{{"role": "user", "content": "Synthetic session isolation regression request."}}, "system": []map[string]string{{"type": "text", "text": "x-anthropic-billing-header: cc_version=2.1.258; cc_entrypoint=cli; cch=00000;"}, {"type": "text", "text": "You are Claude Code, Anthropic's official CLI for Claude."}}, "metadata": map[string]string{"user_id": string(id)}})
	return body
}

func captureIsolatedClaudeRequest(t *testing.T, isolated bool, mode string, payload []byte, headers http.Header, format sdktranslator.Format) claudeIsolationWire {
	t.Helper()
	stream := mode == "stream"
	if stream {
		var err error
		payload, err = sjson.SetBytes(payload, "stream", true)
		if err != nil {
			t.Fatal(err)
		}
	}
	const token = "sk-ant-oat-audit-synthetic-only"
	a := &coreauth.Auth{ID: "claude-isolation-" + t.Name(), Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: claudeOAuthTestMetadata()}
	a.Metadata["access_token"] = token
	a.Metadata["organization_uuid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	a.Metadata["expired"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	a.Metadata["pool_account_id"] = "synthetic-pool-account"
	a.Metadata[claudeAccountProfileTokenSHA256Key] = claudeAccountProfileTokenSHA256(token)
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: isolated, Pools: []config.AccountPool{{ID: "audit-pool", Name: "Synthetic", Provider: "claude", Accounts: []string{coreauth.AccountIdentity(a)}}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-client-a", Pool: "audit-pool"}}}}
	cfg.APIKeys = []string{"synthetic-client-a"}
	m := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
	m.SetConfig(cfg)
	m.SetRetryConfig(0, 0, 0)
	m.RegisterExecutor(NewClaudeExecutor(cfg))
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: "claude-opus-5-5", Type: "claude", Object: "model", OwnedBy: "anthropic", ContextLength: 1000000, MaxCompletionTokens: 64000}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	var captured claudeIsolationWire
	calls := 0
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		expectedPath := "/v1/messages"
		if mode == "count" {
			expectedPath += "/count_tokens"
		}
		if req.URL.Path != expectedPath {
			return nil, fmt.Errorf("unexpected synthetic transport path: %s", req.URL.Path)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		captured = claudeIsolationWire{Headers: req.Header.Clone(), Body: body}
		response := `{"id":"msg_audit","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
		if mode == "count" {
			response = `{"input_tokens":1}`
		}
		ct := "application/json"
		if gjson.GetBytes(body, "stream").Bool() {
			ct = "text/event-stream"
			response = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_audit\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
	})
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
	ctx = coreauth.WithClientIdentity(ctx, "synthetic-client-a", "config-inline")
	opts := coreexecutor.Options{SourceFormat: format, OriginalRequest: payload, Headers: headers, Metadata: map[string]any{coreexecutor.CallerScopeMetadataKey: session.CallerScope("synthetic-client-a")}}
	req := coreexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
	if stream {
		r, err := m.ExecuteStream(ctx, []string{"claude"}, req, opts)
		if err != nil {
			t.Fatal(err)
		}
		for chunk := range r.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		}
	} else if mode == "count" {
		if _, err := m.ExecuteCount(ctx, []string{"claude"}, req, opts); err != nil {
			t.Fatal(err)
		}
	} else {
		if _, err := m.Execute(ctx, []string{"claude"}, req, opts); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("synthetic upstream calls=%d, want exactly one", calls)
	}
	if mode == "count" {
		if gjson.GetBytes(captured.Body, "metadata").Exists() {
			t.Fatal("count_tokens must not include identity metadata")
		}
		return captured
	}
	if gjson.Get(gjson.GetBytes(captured.Body, "metadata.user_id").String(), "device_id").String() != strings.Repeat("0", 64) {
		t.Fatal("credential device identity changed")
	}
	if gjson.Get(gjson.GetBytes(captured.Body, "metadata.user_id").String(), "account_uuid").String() != "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa" {
		t.Fatal("credential account identity changed")
	}
	return captured
}

func TestClaudeIsolationPreservesNativeSessionIdentity(t *testing.T) {
	const parent = "11111111-2222-4333-8444-555555555555"
	const child = "66666666-7777-4888-8999-aaaaaaaaaaaa"
	for _, mode := range []string{"execute", "stream", "count"} {
		t.Run(mode, func(t *testing.T) {
			off := []claudeIsolationWire{}
			on := []claudeIsolationWire{}
			for _, c := range []struct{ id, parent string }{{parent, ""}, {child, parent}} {
				h := claudeNativeHelperHeaders(claudeNativeHelperCoreBetas)
				h.Set("X-Claude-Code-Session-Id", c.id)
				payload := claudeIsolationNativeBody(c.id, c.parent)
				_, d := detectIncomingClaudeCodeRequest(context.Background(), h, payload, false, &config.Config{})
				if !d.Confirmed {
					t.Fatal("fixture must be recognized as native Claude Code")
				}
				off = append(off, captureIsolatedClaudeRequest(t, false, mode, payload, h, sdktranslator.FormatClaude))
				on = append(on, captureIsolatedClaudeRequest(t, true, mode, payload, h, sdktranslator.FormatClaude))
			}
			for i := range on {
				before := gjson.GetBytes(off[i].Body, "metadata.user_id").String()
				after := gjson.GetBytes(on[i].Body, "metadata.user_id").String()
				for _, k := range []string{"Authorization", "User-Agent", "X-Stainless-OS", "X-Stainless-Arch", "X-Stainless-Runtime-Version", "X-Stainless-Package-Version", "Anthropic-Version", "Anthropic-Beta"} {
					if !reflect.DeepEqual(off[i].Headers.Values(k), on[i].Headers.Values(k)) {
						t.Errorf("identity header changed with isolation: %s", k)
					}
				}
				if want := []string{parent, child}[i]; on[i].Headers.Get("X-Claude-Code-Session-Id") != want {
					t.Errorf("outbound session header = %q, want %q", on[i].Headers.Get("X-Claude-Code-Session-Id"), want)
				}
				t.Logf("native=%d off_session=%s on_session=%s on_header_session=%s on_parent=%s", i, gjson.Get(before, "session_id"), gjson.Get(after, "session_id"), on[i].Headers.Get("X-Claude-Code-Session-Id"), gjson.Get(after, "parent_session_id"))
				if gjson.Get(before, "session_id").String() != gjson.Get(after, "session_id").String() {
					t.Errorf("isolation rewrote the preserved native Claude session ID")
				}
			}
			if mode == "count" {
				return
			}
			parentSent := gjson.Get(gjson.GetBytes(on[0].Body, "metadata.user_id").String(), "session_id").String()
			childParent := gjson.Get(gjson.GetBytes(on[1].Body, "metadata.user_id").String(), "parent_session_id").String()
			if parentSent != childParent {
				t.Errorf("child parent_session_id does not match the upstream parent session")
			}
		})
	}
}

func TestClaudeIsolationPreservesCredentialIdentity(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI} {
		for _, mode := range []string{"execute", "stream"} {
			t.Run(fmt.Sprintf("format=%s/mode=%s", format, mode), func(t *testing.T) {
				payload := []byte(`{"model":"claude-opus-5-5","max_tokens":1024,"messages":[{"role":"user","content":"Synthetic session isolation regression request."}]}`)
				headers := http.Header{"User-Agent": {"synthetic-generic-client/1.0"}}
				off := captureIsolatedClaudeRequest(t, false, mode, payload, headers, format)
				on := captureIsolatedClaudeRequest(t, true, mode, payload, headers, format)
				again := captureIsolatedClaudeRequest(t, true, mode, payload, headers, format)
				for _, key := range []string{"Authorization", "User-Agent", "X-Stainless-OS", "X-Stainless-Arch", "X-Stainless-Runtime-Version", "X-Stainless-Package-Version", "Anthropic-Version", "Anthropic-Beta"} {
					if !reflect.DeepEqual(off.Headers.Values(key), on.Headers.Values(key)) {
						t.Errorf("identity header changed: %s", key)
					}
				}
				before := gjson.GetBytes(off.Body, "metadata.user_id").String()
				after := gjson.GetBytes(on.Body, "metadata.user_id").String()
				repeated := gjson.GetBytes(again.Body, "metadata.user_id").String()
				for _, key := range []string{"account_uuid", "device_id"} {
					if gjson.Get(before, key).String() != gjson.Get(after, key).String() {
						t.Errorf("credential identity changed: %s", key)
					}
				}
				if gjson.Get(after, "session_id").String() != gjson.Get(repeated, "session_id").String() {
					t.Error("same key and conversation produced unstable isolated session")
				}
				if !strings.Contains(string(off.Body), "You are Claude Code") || !strings.Contains(string(on.Body), "You are Claude Code") {
					t.Error("existing Claude identity block was lost")
				}
				t.Logf("credential/device/identity headers preserved; generated_session_changed=%v repeated_session_stable=%v", gjson.Get(before, "session_id").String() != gjson.Get(after, "session_id").String(), gjson.Get(after, "session_id").String() == gjson.Get(repeated, "session_id").String())
			})
		}
	}
}
