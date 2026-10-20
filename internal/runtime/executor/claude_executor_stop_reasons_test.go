package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestSyncOctober21ClaudeExecutorChatContextWindowExceededIsHTTP400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"model\":\"claude-opus-5-5\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"cut\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"model_context_window_exceeded\"},\"usage\":{\"output_tokens\":2}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "ctx-window", Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	_, errExecute := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-opus-5-5", Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
	var status interface{ StatusCode() int }
	if !errors.As(errExecute, &status) || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("err = %v, want HTTP 400", errExecute)
	}
	if code := gjson.Get(errExecute.Error(), "error.code").String(); code != "context_length_exceeded" {
		t.Fatalf("error body = %s", errExecute.Error())
	}
}

func TestSyncOctober21ClaudeExecutorChatRefusalStaysSuccessful(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"model\":\"claude-opus-5-5\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"no\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\"},\"usage\":{\"output_tokens\":2}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"))
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{ID: "refusal", Attributes: map[string]string{"api_key": "sk-ant-test", "base_url": server.URL}}
	payload := []byte(`{"model":"claude-opus-5-5","messages":[{"role":"user","content":"hello"}]}`)
	resp, errExecute := NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model: "claude-opus-5-5", Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.finish_reason").String(); got != "content_filter" {
		t.Fatalf("finish_reason = %q: %s", got, resp.Payload)
	}
}
