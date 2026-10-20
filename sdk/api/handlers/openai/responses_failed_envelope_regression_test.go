package openai

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestSyncOctober21ForwardResponsesStreamPreservesResponseFailedEventAndEnvelopeForAllClients(t *testing.T) {
	for _, tc := range []struct {
		name      string
		userAgent string
	}{
		{name: "standard client", userAgent: "openai-python/1.0"},
		{name: "codex client", userAgent: "Codex Desktop/26.803.41515"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, recorder, c, flusher := newResponsesStreamTestHandler(t)
			c.Request.Header.Set("User-Agent", tc.userAgent)

			data := make(chan []byte, 2)
			errs := make(chan *interfaces.ErrorMessage)
			data <- []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg_1_0\",\"type\":\"message\",\"status\":\"incomplete\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}}\n\n")
			data <- []byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":2,\"response\":{\"id\":\"msg_1\",\"object\":\"response\",\"created_at\":1700000000,\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"Claude stopped because the conversation filled the model context window (stop_reason: model_context_window_exceeded).\"},\"output\":[{\"id\":\"msg_1_0\",\"type\":\"message\",\"status\":\"incomplete\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"partial\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":4,\"total_tokens\":14}}}\n\n")
			close(data)
			close(errs)

			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

			body := recorder.Body.String()
			if strings.Count(body, "event: response.failed") != 1 || strings.Contains(body, "event: error") {
				t.Fatalf("expected one response.failed event and no error event, got: %q", body)
			}
			parts := strings.Split(strings.TrimSpace(body), "\n\n")
			last := parts[len(parts)-1]
			payload, ok := responsesSSEDataPayload([]byte(last))
			if !ok {
				t.Fatalf("no data payload in terminal frame: %q", last)
			}
			resp := gjson.GetBytes(payload, "response")
			if resp.Get("id").String() != "msg_1" || resp.Get("status").String() != "failed" {
				t.Fatalf("response envelope lost id or status: %s", payload)
			}
			if resp.Get("error.code").String() != "context_length_exceeded" {
				t.Fatalf("response.error.code = %q, want context_length_exceeded: %s", resp.Get("error.code").String(), payload)
			}
			if resp.Get("output.#").Int() != 1 || resp.Get("usage.total_tokens").Int() != 14 {
				t.Fatalf("response output or usage lost: %s", payload)
			}
		})
	}
}

func TestSyncOctober21ForwardResponsesStreamResponseFailedPreservesLongOutputWhileSanitizingError(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)
	longText := strings.Repeat("a", 3000)
	longEncrypted := strings.Repeat("c", 3000)

	frame := "event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":3,\"response\":{\"id\":\"msg_long\",\"object\":\"response\",\"status\":\"failed\",\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"window exceeded Bearer secret-token-123\"},\"output\":[{\"id\":\"rs_1\",\"type\":\"reasoning\",\"status\":\"completed\",\"encrypted_content\":\"" + longEncrypted + "\",\"summary\":[]},{\"id\":\"msg_1\",\"type\":\"message\",\"status\":\"incomplete\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + longText + "\"}]},{\"id\":\"fc_1\",\"type\":\"function_call\",\"status\":\"incomplete\",\"call_id\":\"call_1\",\"name\":\"write\",\"arguments\":{\"raw\":\"" + strings.Repeat("b", 3000) + "\"}}],\"usage\":{\"input_tokens\":10,\"output_tokens\":20,\"total_tokens\":30}}}\n\n"
	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(frame)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	body := recorder.Body.String()
	payload, ok := responsesSSEDataPayload([]byte(strings.TrimSpace(body)))
	if !ok {
		t.Fatalf("missing data payload: %q", body)
	}
	resp := gjson.GetBytes(payload, "response")
	if got := resp.Get("output.0.encrypted_content").String(); got != longEncrypted {
		t.Fatalf("encrypted_content len = %d, want %d", len(got), len(longEncrypted))
	}
	if got := resp.Get("output.1.content.0.text").String(); got != longText {
		t.Fatalf("output text len = %d, want %d", len(got), len(longText))
	}
	if got := resp.Get("output.2.arguments.raw").String(); len(got) != 3000 {
		t.Fatalf("function_call arguments len = %d, want 3000", len(got))
	}
	if strings.Contains(resp.Get("error.message").String(), "secret-token-123") || !strings.Contains(resp.Get("error.message").String(), "[REDACTED]") {
		t.Fatalf("error.message was not sanitized: %q", resp.Get("error.message").String())
	}
}
