package executor

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeExecutorRejectsEmptiedAttachmentTurn(t *testing.T) {
	for _, part := range []string{`{"type":"file","file":{"file_id":"fixture"}}`, `{"type":"input_audio","input_audio":{"data":"YQ==","format":"wav"}}`} {
		for _, mode := range []string{"execute", "stream", "count"} {
			t.Run(fmt.Sprintf("%s/%s", part, mode), func(t *testing.T) {
				payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"system","content":"Instructions"},{"role":"user","content":[{"type":"text","text":"  "},` + part + `]}]}`)
				u := &midSystemUpstream{}
				ctx := u.context(t, nil)
				ex := NewClaudeExecutor(&config.Config{})
				auth := &cliproxyauth.Auth{ID: "fixture", Attributes: map[string]string{"api_key": "key-fixture", "cloak_mode": "never"}}
				req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload}
				var err error
				switch mode {
				case "execute":
					_, err = ex.Execute(ctx, auth, req, opts)
				case "stream":
					var result *cliproxyexecutor.StreamResult
					result, err = ex.ExecuteStream(ctx, auth, req, opts)
					if result != nil {
						for range result.Chunks {
						}
					}
				case "count":
					_, err = ex.CountTokens(ctx, auth, req, opts)
				}
				var status interface{ StatusCode() int }
				if !errors.As(err, &status) || status.StatusCode() != 400 || u.called {
					t.Fatalf("error=%v upstreamCalled=%t, want local 400", err, u.called)
				}
			})
		}
	}
}

func TestCodexAndCompatExecutorsRejectEmptiedAttachmentTurn(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	for _, tc := range []struct {
		name     string
		executor cliproxyauth.ProviderExecutor
		modes    []string
	}{
		{"codex", NewCodexExecutor(cfg), []string{"execute", "stream", "count"}},
		{"codex-websocket", NewCodexWebsocketsExecutor(cfg), []string{"execute", "stream", "count"}},
		{"openai-compatibility", NewOpenAICompatExecutor("fixture", cfg), []string{"execute", "stream", "count"}},
	} {
		for _, mode := range tc.modes {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				before := calls.Load()
				payload := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"text","text":" "},{"type":"container_upload","file_id":"opaque-file"}]}]}`)
				auth := &cliproxyauth.Auth{ID: t.Name(), Provider: tc.executor.Identifier(), Attributes: map[string]string{"api_key": "fixture", "base_url": upstream.URL}}
				req := cliproxyexecutor.Request{Model: "gpt-5", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}
				var err error
				switch mode {
				case "execute":
					_, err = tc.executor.Execute(t.Context(), auth, req, opts)
				case "stream":
					var result *cliproxyexecutor.StreamResult
					result, err = tc.executor.ExecuteStream(t.Context(), auth, req, opts)
					if result != nil {
						for range result.Chunks {
						}
					}
				case "count":
					_, err = tc.executor.CountTokens(t.Context(), auth, req, opts)
				}
				var status interface{ StatusCode() int }
				if !errors.As(err, &status) || status.StatusCode() != 400 || calls.Load() != before {
					t.Fatalf("error=%v upstreamCalls=%d, want local 400", err, calls.Load())
				}
			})
		}
	}
}

func TestClaudeLocalCountRejectsEmptiedAttachmentTurn(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"opaque-file"}}]}]}`)
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "fixture", "base_url": "http://127.0.0.1:1"}}
	_, err := NewClaudeExecutor(&config.Config{}).CountTokens(t.Context(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != 400 {
		t.Fatalf("error=%v, want local 400", err)
	}
}

func TestCompactExecutorsPreserveNativeAttachments(t *testing.T) {
	cfg := &config.Config{}
	for _, ex := range []cliproxyauth.ProviderExecutor{NewCodexExecutor(cfg), NewCodexWebsocketsExecutor(cfg), NewOpenAICompatExecutor("fixture", cfg)} {
		t.Run(fmt.Sprintf("%T", ex), func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil || gjson.GetBytes(body, "input.0.content.0.file_id").String() != "file-native" {
					t.Errorf("native attachment changed: %s; error=%v", body, errRead)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"compact","object":"response.compaction","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`))
			}))
			defer upstream.Close()
			payload := []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"file-native"}]}]}`)
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "fixture", "base_url": upstream.URL}}
			_, err := ex.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "gpt-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Alt: "responses/compact"})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
