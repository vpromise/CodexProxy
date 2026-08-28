package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCustomMagicHeadersOpenAICompat(t *testing.T) {
	var gotHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	t.Cleanup(server.Close)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":                        server.URL,
			"api_key":                         "test-key",
			"header:X-Claude-Code-Session-Id": "$ABC",
			"header:X-Forwarded-Session":      "$X-Client-Session",
			"header:X-Missing":                "$NONEXISTENT",
			"header:X-Static":                 "static-value",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"messages":[{"role":"user","content":"hi"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Headers: http.Header{
			"Abc":              []string{"session-abc-value"},
			"X-Client-Session": []string{"client-session-uuid-123"},
		},
	}

	if _, errExecute := executor.Execute(context.Background(), auth, req, opts); errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gotHeaders.Get("X-Claude-Code-Session-Id"); got != "session-abc-value" {
		t.Errorf("X-Claude-Code-Session-Id = %q", got)
	}
	if got := gotHeaders.Get("X-Forwarded-Session"); got != "client-session-uuid-123" {
		t.Errorf("X-Forwarded-Session = %q", got)
	}
	if got := gotHeaders.Get("X-Static"); got != "static-value" {
		t.Errorf("X-Static = %q", got)
	}
	if _, exists := gotHeaders["X-Missing"]; exists {
		t.Errorf("expected X-Missing to be omitted")
	}
}
