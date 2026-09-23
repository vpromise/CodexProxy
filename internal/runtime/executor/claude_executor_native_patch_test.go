package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutor_OAuthNativePatchPreservesCallerSystem(t *testing.T) {
	for _, tt := range []struct {
		name       string
		version    string
		cloakMode  string
		wantNative bool
	}{
		{"previous baseline", "2.1.258", "", true},
		{"current baseline", "2.1.280", "", true},
		{"next patch", "2.1.281", "", true},
		{"newer patch", "2.1.263", "", true},
		{"older patch", "2.1.257", "", false},
		{"unmeasured minor", "2.2.0", "", false},
		{"explicit cloak", "2.1.263", "always", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			type observedRequest struct {
				body    []byte
				headers http.Header
			}
			requests := make(chan observedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					http.Error(w, errRead.Error(), http.StatusBadRequest)
					return
				}
				requests <- observedRequest{body: body, headers: r.Header.Clone()}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"msg_patch","type":"message","model":"claude-sonnet-4-6","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer server.Close()

			const userID = `{"device_id":"0000000000000000000000000000000000000000000000000000000000000000","account_uuid":"aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa","session_id":"11111111-2222-4333-8444-555555555555"}`
			payload := []byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"caller-system","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":` + fmt.Sprintf("%q", userID) + `}}`)
			userAgent := "claude-cli/" + tt.version + " (external, cli)"
			headers := http.Header{
				"User-Agent":     {userAgent},
				"X-App":          {"cli"},
				"Anthropic-Beta": {"claude-code-20250219,interleaved-thinking-2025-05-14"},
			}
			auth := &cliproxyauth.Auth{
				ID:       "oauth-native-patch-" + tt.name,
				Provider: "claude",
				Attributes: map[string]string{
					"api_key":    "sk-ant-oat01-native-patch-test",
					"base_url":   server.URL,
					"cloak_mode": tt.cloakMode,
				},
				Metadata: claudeOAuthTestMetadata(),
			}
			executor := NewClaudeExecutor(&config.Config{})
			_, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model: "claude-sonnet-4-6", Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: headers,
			})
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			seen := <-requests
			wantUA := "claude-cli/2.1.280 (external, cli)"
			if tt.wantNative {
				wantUA = userAgent
			}
			if got := seen.headers.Get("User-Agent"); got != wantUA {
				t.Errorf("User-Agent = %q, want %q", got, wantUA)
			}
			// OAuth adds a billing signature block even for native clients.
			// Check caller placement independently of its absolute block index.
			preservedCallerSystem := false
			for _, block := range gjson.GetBytes(seen.body, "system").Array() {
				if block.Get("text").String() == "caller-system" {
					preservedCallerSystem = true
				}
			}
			if preservedCallerSystem != tt.wantNative {
				t.Errorf("caller system preserved = %v, want %v", preservedCallerSystem, tt.wantNative)
			}
		})
	}
}
