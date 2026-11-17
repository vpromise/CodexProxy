package openai

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/tidwall/gjson"
)

func TestResponsesSyncWrappedErrorFramingAndRedaction(t *testing.T) {
	for _, codex := range []bool{false, true} {
		framer := &responsesSSEFramer{isCodexClient: codex}
		if codex {
			framer.failureEvent = "response.failed"
		}
		payload := `{"sequence_number":17,"body":{"status":400,"error":{"type":"invalid_request_error","code":"fixture_error","message":"rejected api_key=fixture-secret","param":"token=fixture-param"}},"debug":{"token":"private-debug"}}`
		out := framer.repairFrame([]byte("data: " + payload + "\n\n"))
		if framer.terminalError == nil || framer.terminalError.StatusCode != http.StatusBadRequest {
			t.Fatalf("wrapped error not terminal 400: %s", out)
		}
		if strings.Contains(string(out), "fixture-secret") || strings.Contains(string(out), "fixture-param") || strings.Contains(string(out), "private-debug") {
			t.Errorf("secret leaked: %s", out)
		}
		data, ok := responsesSSEDataPayload(out)
		if !ok {
			t.Fatal("no data")
		}
		path := "message"
		if codex {
			path = "response.error.message"
		}
		if gjson.GetBytes(data, "sequence_number").Int() != 17 {
			t.Errorf("sequence lost: %s", out)
		}
		if !strings.Contains(gjson.GetBytes(data, path).String(), "rejected") || !strings.Contains(gjson.GetBytes(data, path).String(), "[REDACTED]") {
			t.Errorf("message lost: %s", out)
		}
	}
}

func TestResponsesSyncBodyErrorDoesNotInferMetadataFailure(t *testing.T) {
	for _, frame := range []string{
		"event: response.metadata\ndata: {\"body\":{\"error\":{\"message\":\"diagnostic\"}}}\n\n",
		"data: {\"type\":\"response.metadata\",\"body\":{\"error\":\"diagnostic\"}}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"text\",\"body\":{\"error\":\"quoted\"}}\n\n",
	} {
		framer := &responsesSSEFramer{isCodexClient: true}
		out := framer.repairFrame([]byte(frame))
		if framer.terminalError != nil || string(out) != frame {
			t.Errorf("explicit non-error changed: %s", out)
		}
	}
}

func TestResponsesSyncScalarSanitization(t *testing.T) {
	for _, payload := range []string{
		`{"error":"rejected api_key=fixture-secret"}`,
		`{"body":{"error":"rejected api_key=fixture-secret"}}`,
	} {
		safe := responsesStreamErrorText(&interfaces.ErrorMessage{StatusCode: 400, Error: errors.New(payload)}, 400)
		if strings.Contains(safe, "fixture-secret") || !strings.Contains(safe, "rejected") || !strings.Contains(safe, "[REDACTED]") {
			t.Errorf("scalar error lost or unmasked: %s", safe)
		}
	}
}
