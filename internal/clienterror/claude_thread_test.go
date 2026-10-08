package clienterror

import (
	"errors"
	"fmt"
	"testing"
)

func TestClaudeMissingThreadRequestFault(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       bool
	}{
		{"missing", `{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id."}}`, 404, true},
		{"already-marked", `{"error":{"type":"not_found_error","details":{"error_code":"thread_not_found"},"message":"Please replay history"}}`, 404, true},
		{"plaintext", `No thread state was found for the requested previous_message_id.`, 404, false},
		{"model", `{"error":{"type":"not_found_error","message":"model not found"}}`, 404, false},
		{"unrelated", `{"error":{"type":"not_found_error","message":"thread state unavailable"}}`, 404, false},
		{"unauthorized", `{"error":{"type":"not_found_error","message":"No thread state for previous_message_id"}}`, 401, false},
		{"limited", `{"error":{"type":"not_found_error","message":"No thread state for previous_message_id"}}`, 429, false},
		{"server", `{"error":{"type":"not_found_error","message":"No thread state for previous_message_id"}}`, 500, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRequestFault(tc.status, errors.New(tc.body)); got != tc.want {
				t.Fatalf("request fault=%v want %v", got, tc.want)
			}
		})
	}
}

func TestClaudeMissingThreadWrappedBody(t *testing.T) {
	err := fmt.Errorf("upstream failed: %w", threadFaultBodyError{})
	if !IsRequestFault(0, err) {
		t.Fatal("wrapped status and response body were not classified")
	}
}

type threadFaultBodyError struct{}

func (threadFaultBodyError) Error() string   { return "opaque upstream error" }
func (threadFaultBodyError) StatusCode() int { return 404 }
func (threadFaultBodyError) ResponseBody() []byte {
	return []byte(`{"error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id."}}`)
}
