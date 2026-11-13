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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatTypedContentExecute(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"typed","object":"chat.completion","choices":[{"index":0,"finish_reason":"tool_calls","message":{"reasoning_content":"preferred","content":[{"type":"thinking","thinking":"duplicate"},{"type":"text","text":"answer"},{"type":"unknown","payload":"do-not-leak"}],"tool_calls":[{"id":"call_read","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}}]}}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	request := []byte(`{"model":"client-alias","input":"hello","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}`)
	result, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "private-compatible-model", Payload: request,
		Metadata: map[string]any{"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{ID: "private-compatible-model", IsCompat: true}},
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: request})
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "private-compatible-model" {
		t.Fatalf("upstream model = %q", got)
	}
	data := gjson.ParseBytes(result.Payload)
	for path, want := range map[string]string{
		"model": "private-compatible-model", "output.0.type": "reasoning", "output.0.summary.0.text": "preferred",
		"output.1.content.0.text": "answer", "output.2.type": "function_call", "output.2.call_id": "call_read",
		"output.2.name": "read", "output.2.arguments": `{"path":"x"}`, "usage.total_tokens": "7",
	} {
		if got := data.Get(path).String(); got != want {
			t.Errorf("%s = %q, want %q; response=%s", path, got, want, result.Payload)
		}
	}
	if strings.Contains(string(result.Payload), "duplicate") || strings.Contains(string(result.Payload), "do-not-leak") {
		t.Fatalf("structured content leaked: %s", result.Payload)
	}
}

func TestOpenAICompatTypedContentExecuteStream(t *testing.T) {
	for _, tc := range []struct {
		name         string
		delta        string
		finish       string
		suffix       string
		wantTerminal string
		wantError    string
	}{
		{name: "typed reasoning clean EOF", delta: `{"content":[{"type":"thinking","thinking":[{"type":"text","text":"plan"}]}]}`, finish: "stop", wantTerminal: "response.completed"},
		{name: "typed reasoning truncated EOF", delta: `{"content":[{"type":"reasoning","text":"plan"}]}`, finish: "length", wantTerminal: "response.incomplete"},
		{name: "legacy string reasoning EOF", delta: `{"reasoning_content":"plan"}`, finish: "stop", wantTerminal: "response.completed"},
		{name: "interleaved DONE", delta: `{"content":[{"type":"text","text":"first"},{"type":"thinking","thinking":"plan"},{"type":"text","text":"last"},{"type":"unknown","payload":"do-not-leak"}]}`, finish: "stop", suffix: "data: [DONE]\n\n", wantTerminal: "response.completed"},
		{name: "missing finish reason EOF", delta: `{"content":[{"type":"thinking","thinking":"plan"}]}`, wantError: "closed before [DONE]"},
		{name: "upstream error after finish", delta: `{"content":[{"type":"thinking","thinking":"plan"}]}`, finish: "stop", suffix: "event: error\ndata: {\"error\":{\"message\":\"explicit-upstream-failure\",\"type\":\"server_error\"}}\n\ndata: [DONE]\n\n", wantError: "explicit-upstream-failure"},
		{name: "unfinished tool prevents EOF", delta: `{"content":[{"type":"thinking","thinking":"plan"}],"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{"}}]}`, wantError: "closed before [DONE]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprintf(w, "data: {\"id\":\"typed\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":%s}]}\n\n", tc.delta)
				if tc.finish != "" {
					_, _ = fmt.Fprintf(w, "data: {\"id\":\"typed\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\n", tc.finish)
				}
				_, _ = io.WriteString(w, "data: {\"id\":\"typed\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":4,\"total_tokens\":7}}\n\n"+tc.suffix)
			}))
			defer server.Close()
			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
			request := []byte(`{"model":"client-alias","input":"hello","stream":true}`)
			result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{Model: "private-compatible-model", Payload: request}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: request, Stream: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			var streamErr error
			for chunk := range result.Chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if tc.wantError != "" {
				if streamErr == nil || !strings.Contains(streamErr.Error(), tc.wantError) {
					t.Fatalf("stream error = %v, want %q; output=%s", streamErr, tc.wantError, output.String())
				}
				if strings.Contains(output.String(), "event: response.completed") || strings.Contains(output.String(), "event: response.incomplete") {
					t.Fatalf("failure emitted successful terminal: %s", output.String())
				}
			} else {
				if streamErr != nil {
					t.Fatalf("stream error = %v; output=%s", streamErr, output.String())
				}
				var terminal gjson.Result
				count := 0
				for _, line := range strings.Split(output.String(), "\n") {
					if !strings.HasPrefix(line, "data: ") {
						continue
					}
					event := gjson.Parse(strings.TrimPrefix(line, "data: "))
					if event.Get("type").String() == tc.wantTerminal {
						terminal = event
						count++
					}
				}
				if count != 1 {
					t.Fatalf("terminal count = %d, want 1; output=%s", count, output.String())
				}
				if got := terminal.Get("response.usage.total_tokens").Int(); got != 7 {
					t.Fatalf("late usage = %d; output=%s", got, output.String())
				}
				if got := terminal.Get("response.model").String(); got != "client-alias" {
					t.Fatalf("response model = %q", got)
				}
				if tc.name == "interleaved DONE" {
					if got := terminal.Get("response.output.#").Int(); got != 3 {
						t.Fatalf("output count = %d; output=%s", got, output.String())
					}
				} else if got := terminal.Get("response.output.0.summary.0.text").String(); got != "plan" {
					t.Fatalf("reasoning = %q; output=%s", got, output.String())
				}
			}
			if strings.Contains(output.String(), "do-not-leak") {
				t.Fatalf("structured content leaked: %s", output.String())
			}
		})
	}
}
