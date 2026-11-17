package handlers

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestResponsesSyncErrorEnvelopeMessages(t *testing.T) {
	for _, envelope := range []string{
		`{"type":"error","error":"original failure","sequence_number":17}`,
		`{"type":"error","body":{"error":"original failure"},"sequence_number":17}`,
		`{"type":"error","error":null,"body":{"error":{"message":"original failure","code":"fixture_error"}},"sequence_number":17}`,
		`{"type":"response.failed","response":{"error":{"message":"original failure","code":"fixture_error"}},"sequence_number":17}`,
	} {
		t.Run(envelope, func(t *testing.T) {
			for _, seq := range []int{0, 4} {
				legacy := BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, envelope, seq)
				failed := BuildOpenAIResponsesStreamFailedChunk(http.StatusBadRequest, envelope, seq)
				if gjson.GetBytes(legacy, "message").String() != "original failure" {
					t.Errorf("legacy lost message: %s", legacy)
				}
				if gjson.GetBytes(failed, "response.error.message").String() != "original failure" {
					t.Errorf("Codex lost message: %s", failed)
				}
				wantSeq := int64(seq)
				if seq == 0 {
					wantSeq = 17
				}
				if gjson.GetBytes(legacy, "sequence_number").Int() != wantSeq || gjson.GetBytes(failed, "sequence_number").Int() != wantSeq {
					t.Errorf("sequence precedence changed: %s / %s", legacy, failed)
				}
				if gjson.GetBytes(legacy, "type").String() != "error" || gjson.GetBytes(legacy, "error").Exists() || gjson.GetBytes(failed, "type").String() != "response.failed" {
					t.Error("legacy/native shape changed")
				}
			}
		})
	}
}

func TestResponsesSyncErrorDetailsKeepNumericPrecision(t *testing.T) {
	envelope := `{"error":{"message":"failure","code":"fixture_error","param":{"counter":9007199254740993,"amount":1.234567890123456789}},"sequence_number":17}`
	failed := BuildOpenAIResponsesStreamFailedChunk(http.StatusBadRequest, envelope, 0)
	for _, path := range []string{"counter", "amount"} {
		if got, want := gjson.GetBytes(failed, "response.error.param."+path).Raw, gjson.Get(envelope, "error.param."+path).Raw; got != want {
			t.Errorf("%s=%s want %s", path, got, want)
		}
	}
}
