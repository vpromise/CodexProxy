package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSyncOctober21ClaudeExecutor_ConfirmedSDKTSOAuthPreservesToolNames(t *testing.T) {
	var seenBody []byte
	var seenHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenBody, _ = io.ReadAll(r.Body)
		seenHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"claude-opus-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
	}))
	defer server.Close()

	const userID = `{"device_id":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","account_uuid":"","session_id":"33333333-4444-4555-8666-777777777777"}`
	payload := []byte(`{"model":"claude-opus-4-6","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.258.0a8; cc_entrypoint=sdk-ts; cch=00000;"}],"tools":[{"name":"bash","description":"known native name must pass through","input_schema":{"type":"object"}},{"name":"search_web","description":"unknown native name must pass through","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"x"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
	deviceIDs := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
	}
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-native-sdk-ts",
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"account_uuid":      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			"claude_device_ids": deviceIDs,
			"cloak_mode":        "auto",
		},
	}
	_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-4-6",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers: http.Header{
			"User-Agent":                  {"claude-cli/2.1.258 (external, sdk-ts, agent-sdk/0.3.252)"},
			"X-App":                       {"cli"},
			"Anthropic-Beta":              {"claude-code-20250219"},
			"X-Stainless-Package-Version": {"0.112.1"},
			"X-Stainless-Runtime-Version": {"v26.3.0"},
		},
	})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}

	if got := gjson.GetBytes(seenBody, "tools.0.name").String(); got != "bash" {
		t.Fatalf("tools.0.name = %q, want confirmed native known name preserved", got)
	}
	if got := gjson.GetBytes(seenBody, "tools.1.name").String(); got != "search_web" {
		t.Fatalf("tools.1.name = %q, want confirmed native unknown name preserved", got)
	}
	assertClaudeCredentialIdentity(t, seenBody, seenHeaders, deviceIDs, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	upstreamUserID := gjson.GetBytes(seenBody, "metadata.user_id").String()
	if upstreamDeviceID := gjson.Get(upstreamUserID, "device_id").String(); upstreamDeviceID == strings.Repeat("c", 64) {
		t.Fatalf("device_id = %q, want native device replaced by credential pool", upstreamDeviceID)
	}
	if got := gjson.Get(upstreamUserID, "session_id").String(); got != "33333333-4444-4555-8666-777777777777" {
		t.Fatalf("session_id = %q, want downstream agent session", got)
	}
	if got := seenHeaders.Get("X-Claude-Code-Session-Id"); got != "33333333-4444-4555-8666-777777777777" {
		t.Fatalf("X-Claude-Code-Session-Id = %q, want downstream agent session", got)
	}
}
