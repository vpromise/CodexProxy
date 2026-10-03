package executor

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAICompatExecutor_NormalizesToolIntegerTypesForCodexUserAgent_NonCodexTarget(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","created":1700000000,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}

	requestPayload := []byte(`{
		"model": "test-model",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [
			{
				"type": "function",
				"function": {
					"name": "exec_command",
					"parameters": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"},
							"timeout_ms": {"type": "number"}
						}
					}
				}
			}
		]
	}`)

	_, errExec := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "test-model",
		Payload: requestPayload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Headers:      http.Header{"User-Agent": []string{"codex-tui/0.154.0"}},
		Stream:       false,
	})
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}

	if got := gjson.GetBytes(gotBody, "tools.0.function.parameters.properties.yield_time_ms.type").String(); got != "integer" {
		t.Fatalf("expected non-Codex executor to normalize yield_time_ms to integer, got: %s (body=%s)", got, string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "tools.0.function.parameters.properties.timeout_ms.type").String(); got != "integer" {
		t.Fatalf("expected non-Codex executor to normalize timeout_ms to integer, got: %s (body=%s)", got, string(gotBody))
	}
}
