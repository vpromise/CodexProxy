package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func newTruncationCompatTestExecutor(t *testing.T, reply string) (*OpenAICompatExecutor, *cliproxyauth.Auth, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, reply)
	}))
	t.Cleanup(server.Close)
	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	return exec, auth, server
}
func TestClaudeExecutorStreamTruncationReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		// Abrupt clean EOF: no message_stop, no error event.
	}))
	defer server.Close()

	pluginName := "test-claude-passthrough-truncation"
	plugin := &captureClaudeUsagePlugin{
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
	})

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunkErr error
	var sawMessageStop bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
			continue
		}
		if strings.Contains(string(chunk.Payload), "message_stop") {
			sawMessageStop = true
		}
	}

	if chunkErr == nil {
		t.Fatalf("expected a truncation error chunk when upstream closes before message_stop, got none (sawMessageStop=%v)", sawMessageStop)
	}
	if !strings.Contains(chunkErr.Error(), "message_stop") {
		t.Errorf("truncation error = %q, want it to mention message_stop", chunkErr.Error())
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("expected usage record to be marked failed on truncated stream")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for failed usage record")
	}
}

func TestClaudeExecutorTranslatedStreamTruncationReportsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	pluginName := "test-claude-translated-truncation"
	plugin := &captureClaudeUsagePlugin{
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
	})

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunkErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			chunkErr = chunk.Err
		}
	}

	if chunkErr == nil {
		t.Fatal("expected a truncation error chunk on translated path when upstream closes before message_stop, got none")
	}
	if !strings.Contains(chunkErr.Error(), "message_stop") {
		t.Errorf("truncation error = %q, want it to mention message_stop", chunkErr.Error())
	}

	select {
	case record := <-plugin.records:
		if !record.Failed {
			t.Fatalf("expected usage record to be marked failed on truncated translated stream")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for failed usage record")
	}
}

func TestClaudeExecutorStreamCompleteNoError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "event: message_start\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")
		_, _ = fmt.Fprint(w, "event: content_block_delta\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		_, _ = fmt.Fprint(w, "event: message_stop\n")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"message_stop\"}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer server.Close()

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected error chunk in complete stream: %v", chunk.Err)
		}
	}
}

func TestOpenAICompatExecutorClaudeEOFBeforeFinishReasonFailsStream(t *testing.T) {
	reply := "data: {\"id\":\"chatcmpl-truncated\",\"object\":\"chat.completion.chunk\",\"created\":1773896263,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}]}\n\n"
	exec, auth, _ := newTruncationCompatTestExecutor(t, reply)
	request := []byte(`{"model":"test","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`)

	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"type":"content_block_delta"`) {
		t.Fatalf("truncated Claude stream did not deliver its valid content frame: %q", output.String())
	}
	if streamErr == nil {
		t.Fatalf("expected error chunk on truncated OpenAI-to-Claude stream missing finish_reason, got nil; output=%q", output.String())
	}
	if strings.Contains(output.String(), "message_stop") {
		t.Fatalf("truncated OpenAI-to-Claude stream must not synthesize message_stop: %q", output.String())
	}
}

func TestOpenAICompatExecutorClaudeEOFAfterFinishReasonCompletesStream(t *testing.T) {
	reply := strings.Join([]string{
		`data: {"id":"chatcmpl-eof","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":null}]}`,
		"",
		`data: {"id":"chatcmpl-eof","object":"chat.completion.chunk","created":1773896263,"model":"test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		"",
	}, "\n")
	exec, auth, _ := newTruncationCompatTestExecutor(t, reply)
	request := []byte(`{"model":"test","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`)

	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: request,
		Stream:          true,
	})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}

	var output bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error after finish_reason was received: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), "message_stop") {
		t.Fatalf("expected message_stop when finish_reason was received before clean EOF: %q", output.String())
	}
}

func TestClaudeExecutorMultiLineDataMessageStopAndErrorEvents(t *testing.T) {
	multiLineSuccess := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\n" +
		"data: \"message\":{\"id\":\"msg_multi_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: message_stop\n" +
		"data: {\"type\":\n" +
		"data: \"message_stop\"}\n\n"

	if gotID := claudeMessageIDFromSSE([]byte(multiLineSuccess)); gotID != "msg_multi_1" {
		t.Fatalf("claudeMessageIDFromSSE() on multi-line SSE = %q, want %q", gotID, "msg_multi_1")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, multiLineSuccess)
	}))
	defer server.Close()

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": server.URL,
	}}
	result, err := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected error on multi-line message_stop stream: %v", chunk.Err)
		}
	}

	nestedMultiLineTruncated := "event: message_start\n" +
		"data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_nested_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-opus-5\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"event: content_block_start\n" +
		"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"emit\",\"input\":\n" +
		"data: {\"type\":\"message_stop\"}\n" +
		"data: }}\n\n"

	if gotID := claudeMessageIDFromSSE([]byte(nestedMultiLineTruncated)); gotID != "" {
		t.Fatalf("claudeMessageIDFromSSE() on truncated stream with nested message_stop line = %q, want empty string", gotID)
	}

	truncServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, nestedMultiLineTruncated)
	}))
	defer truncServer.Close()

	truncResult, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-123",
		"base_url": truncServer.URL,
	}}, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var nestedChunkErr error
	for chunk := range truncResult.Chunks {
		if chunk.Err != nil {
			nestedChunkErr = chunk.Err
		}
	}
	if nestedChunkErr == nil {
		t.Fatal("expected truncation error when stream ends after multiline tool_use with nested message_stop line, got nil")
	}
}

type captureClaudeUsagePlugin struct{ records chan usage.Record }

func (p *captureClaudeUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider == "claude" {
		select {
		case p.records <- record:
		default:
		}
	}
}

type noopClaudeUsagePlugin struct{}

func (noopClaudeUsagePlugin) HandleUsage(context.Context, usage.Record) {}

func TestClaudeExecutorMultiLineTerminalPreservesClassification(t *testing.T) {
	for _, format := range []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAIResponse} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/error=%t", format, failed), func(t *testing.T) {
				body := "event: message_start\ndata: {\"type\":\"message_start\",\ndata: \"message\":{\"id\":\"msg_multiline\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"usage\":{\"input_tokens\":7,\"output_tokens\":0}}}\n\n"
				if failed {
					body += "event: error\ndata: {\"type\":\"error\",\ndata: \"error\":{\"type\":\"rate_limit_error\",\"message\":\"fixture rate limit\"}}\n\n"
				} else {
					body += "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":3}}\n\nevent: message_stop\ndata: {\"type\":\ndata: \"message_stop\"}\n\n"
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, body)
				}))
				defer server.Close()
				auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fixture", "base_url": server.URL}}
				result, err := NewClaudeExecutor(&config.Config{}).ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "claude-opus-5", Payload: []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: format})
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				var streamErr error
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						streamErr = chunk.Err
					}
					output.Write(chunk.Payload)
				}
				if failed {
					status, ok := streamErr.(interface{ StatusCode() int })
					if !ok || status.StatusCode() != http.StatusTooManyRequests {
						t.Fatalf("classification lost: %T %v", streamErr, streamErr)
					}
					if strings.Contains(output.String(), "message_stop") || strings.Contains(output.String(), "response.completed") {
						t.Fatalf("error completed stream: %s", output.String())
					}
				} else {
					terminal := "message_stop"
					if format == sdktranslator.FormatOpenAIResponse {
						terminal = "response.completed"
					}
					if streamErr != nil || !strings.Contains(output.String(), terminal) {
						t.Fatalf("completion lost: %v, %s", streamErr, output.String())
					}
				}
			})
		}
	}
}
