package claude

import (
	"errors"
	"fmt"
	"testing"
)

func TestRefreshHTTPErrorExposesStatusWithoutChangingRetryPolicy(t *testing.T) {
	for _, code := range []int{400, 401, 429, 503} {
		err := &refreshHTTPError{status: code, message: "fixture", retryable: code >= 500}
		var status interface{ StatusCode() int }
		if !errors.As(fmt.Errorf("wrapped: %w", err), &status) || status.StatusCode() != code {
			t.Errorf("status %d not exposed", code)
		}
		if got := isClaudeRefreshRetryable(err); got != (code >= 500) {
			t.Errorf("status %d retryable=%v", code, got)
		}
	}
}
