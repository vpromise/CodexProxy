package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestOpenAICompatExecutorPropagatesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"rate_limit","message":"try later"}}`))
	}))
	t.Cleanup(server.Close)

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	request := cliproxyexecutor.Request{
		Model:   "compatible-model",
		Payload: []byte(`{"model":"compatible-model","messages":[{"role":"user","content":"hi"}]}`),
	}
	tests := []struct {
		name   string
		invoke func() error
	}{
		{
			name: "nonstream",
			invoke: func() error {
				_, errExecute := executor.Execute(context.Background(), auth, request, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai"),
				})
				return errExecute
			},
		},
		{
			name: "stream bootstrap",
			invoke: func() error {
				_, errExecute := executor.ExecuteStream(context.Background(), auth, request, cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FromString("openai"),
					Stream:       true,
				})
				return errExecute
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errExecute := test.invoke()
			if errExecute == nil {
				t.Fatal("expected rate-limit error")
			}
			retryable, ok := errExecute.(interface{ RetryAfter() *time.Duration })
			if !ok || retryable.RetryAfter() == nil || *retryable.RetryAfter() != 7*time.Second {
				t.Fatalf("retry-after = %v, want 7s", retryable)
			}
		})
	}
}
