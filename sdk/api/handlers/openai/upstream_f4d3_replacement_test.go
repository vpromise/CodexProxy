package openai

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeResponsesWebsocketRequestReplacesTranscriptWhenPendingOutputIsMissingWithoutIncrementalUpstream(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","role":"user","id":"summary-1","content":"compacted summary"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(raw, lastRequest, lastResponseOutput, "resp-1", []string{"call-1"}, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not be set on a transcript replacement: %s", normalized)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "summary-1" {
		t.Fatalf("replacement must not merge the stale turn with its unanswered function_call: %s", normalized)
	}
	if got := gjson.GetBytes(normalized, "model").String(); got != "test-model" {
		t.Fatalf("model = %q, want test-model", got)
	}
	if got := gjson.GetBytes(normalized, "instructions").String(); got != "be helpful" {
		t.Fatalf("instructions = %q, want be helpful", got)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestMergesWhenPendingOutputIsPresentWithoutIncrementalUpstream(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(raw, lastRequest, lastResponseOutput, "resp-1", []string{"call-1"}, false, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 3 || input[0].Get("id").String() != "msg-1" || input[1].Get("id").String() != "fc-1" || input[2].Get("id").String() != "tool-out-1" {
		t.Fatalf("answered pending call must keep the merged transcript: %s", normalized)
	}
}
