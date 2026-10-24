package executor

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func claudeIsolationContexts(t *testing.T) (context.Context, context.Context, *cliproxyauth.Auth) {
	t.Helper()
	auth := &cliproxyauth.Auth{ID: t.Name() + "/" + uuid.NewString(), Provider: "claude", Metadata: claudeOAuthTestMetadata()}
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{
		Enabled: true,
		Pools:   []config.AccountPool{{ID: "shared", Name: "Shared", Provider: "claude", Accounts: []string{cliproxyauth.AccountIdentity(auth)}}},
		Keys:    []config.ClientKeyBinding{{Key: "client-a", Pool: "shared"}, {Key: "client-b", Pool: "shared"}},
	}}
	cfg.APIKeys = []string{"client-a", "client-b"}
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	caller := func(key string) context.Context {
		ctx := manager.NewAccessRequest(cliproxyauth.WithClientIdentity(t.Context(), key, "config-inline"))
		if !cliproxyauth.AccessScopeFromContext(ctx).Allows(auth) {
			t.Fatal("fixture must authorize both keys for the shared credential")
		}
		ctx = helps.WithClaudeSessionID(ctx, "11111111-2222-4333-8444-555555555555")
		return helps.WithClaudeExecutionMetadata(ctx, true)
	}
	return caller("client-a"), caller("client-b"), auth
}

func TestClaudeIsolationKeepsFallbackDiagnosticsPrivate(t *testing.T) {
	a, b, auth := claudeIsolationContexts(t)
	const sessionID = "11111111-2222-4333-8444-555555555555"
	payload := []byte(`{"messages":[]}`)
	_, first := injectClaudeDiagnostics(a, payload, auth, sessionID)
	commitClaudeDiagnostics(first, "msg_client_a")
	other, otherState := injectClaudeDiagnostics(b, payload, auth, sessionID)
	if got := gjson.GetBytes(other, "diagnostics.previous_message_id"); got.Type != gjson.Null {
		t.Fatal("fallback diagnostics inherited another caller's message")
	}
	commitClaudeDiagnostics(otherState, "msg_client_b")
	continued, _ := injectClaudeDiagnostics(a, payload, auth, sessionID)
	if got := gjson.GetBytes(continued, "diagnostics.previous_message_id").String(); got != "msg_client_a" {
		t.Fatal("fallback diagnostics lost the original caller's message")
	}
}

func TestClaudeIsolationKeepsContinuityPrivateWithSharedNativeSession(t *testing.T) {
	a, b, auth := claudeIsolationContexts(t)
	payload := claudeIsolationNativeBody("11111111-2222-4333-8444-555555555555", "")
	resolve := func(ctx context.Context) helps.ClaudeContinuityContext {
		_, _, state, ok := resolveClaudeContinuityTags(ctx, auth, nil, payload, true, "", "")
		if !ok {
			t.Fatal("continuity was not initialized")
		}
		return state
	}
	first := resolve(a)
	helps.CommitClaudeContinuity(first.Key, first.Sequence, "msg_client_a", "req_client_a", first.PromptID)
	other := resolve(b)
	if other.PreviousMessageID != "" || other.PreviousRequestID != "" || other.Key == first.Key {
		t.Fatal("another key inherited the first caller's continuity")
	}
	helps.CommitClaudeContinuity(other.Key, other.Sequence, "msg_client_b", "req_client_b", other.PromptID)
	// A tool continuation reuses the prompt ID; a new user prompt gets a new one.
	payload = []byte(`{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"done"}]}]}`)
	continued := resolve(a)
	if continued.PreviousMessageID != "msg_client_a" || continued.PreviousRequestID != "req_client_a" || continued.PromptID != first.PromptID {
		t.Fatal("the original caller lost its own continuity")
	}
}
