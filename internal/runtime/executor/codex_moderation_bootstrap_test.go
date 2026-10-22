package executor

import (
	"context"
	"net/http"
	"testing"
)

// Captured from upstream on 2026-10-21: created, in_progress, a prompt-moderation response.metadata
// frame, then server_is_overloaded. Nothing generated has been produced, so the attempt must fail over.
const codexModerationMetadataEvent = `{"type":"response.metadata","response_id":"resp_1","sequence_number":2,"metadata":{"openai_chatgpt_moderation_metadata":{"prompt":{"omnimod":{"protection_type":"omnimod"}}}}}`

func TestCodexExecutor_BootstrapBuffering_OverloadAfterModerationMetadataFailsAttempt(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexModerationMetadataEvent, codexOverloadEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err == nil {
		t.Fatal("expected ExecuteStream to fail the attempt on an overload after response.metadata")
	}
	if result != nil {
		t.Fatal("expected nil result so no buffered frame can reach the client")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_OverloadAfterModerationMetadataFailsAttempt(t *testing.T) {
	server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, codexModerationMetadataEvent, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err == nil {
		t.Fatal("expected ExecuteStream to fail the attempt on a websocket overload after response.metadata")
	}
	if result != nil {
		t.Fatal("expected nil result so no buffered frame can reach the client")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
	}
}
