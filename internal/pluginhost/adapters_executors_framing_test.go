package pluginhost

import (
	"context"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"strings"
	"testing"
)

// TestExecutorAdapterExecuteStreamSameFormatStripsSSEFraming pins the contract
// that same-format plugin executor streams are normalized like built-in
// executor streams: SSE-framed chunks ("data: {...}\n\n") must reach callers
// as bare JSON payloads without the "[DONE]" marker, because the HTTP handlers
// add their own "data:" prefix and terminal marker. Forwarding the framed
// chunks unchanged produced a doubled "data: data:" prefix on the wire.
func TestExecutorAdapterExecuteStreamSameFormatStripsSSEFraming(t *testing.T) {
	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream-framing"})
	host := newHostWithRecords(executorRecord)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 4)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: [DONE]\n\n")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-stream-framing",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: streamChunks}, nil
		},
	}

	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-stream-framing"

	auth := &coreauth.Auth{ID: "auth-stream-framing-1", Provider: "plugin-provider-stream-framing"}
	req := coreexecutor.Request{
		Model:   "test-model",
		Payload: []byte(`{"model":"test-model","stream":true}`),
	}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	}

	streamRes, errStream := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", errStream)
	}

	var received []string
	for chunk := range streamRes.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		received = append(received, string(chunk.Payload))
	}

	want := []string{
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	if len(received) != len(want) {
		t.Fatalf("received %d chunks %q, want %d %q", len(received), received, len(want), want)
	}
	for i := range want {
		if received[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, received[i], want[i])
		}
		if strings.HasPrefix(received[i], "data:") {
			t.Errorf("chunk %d still carries SSE framing: %q", i, received[i])
		}
	}
}
