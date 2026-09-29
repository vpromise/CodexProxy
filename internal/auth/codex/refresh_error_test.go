package codex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestRefreshInvalidGrantDoesNotRetry(t *testing.T) {
	for _, body := range []string{`{"error":"invalid_grant"}`, `{"error":{"code":"invalid_grant","message":"expired credential"}}`} {
		t.Run(body, func(t *testing.T) {
			calls := 0
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}}
			_, err := auth.RefreshTokensWithRetry(context.Background(), "fixture-invalid-grant-"+body, 3)
			if err == nil {
				t.Fatal("expected refresh error")
			}
			if calls != 1 {
				t.Errorf("token requests=%d want 1", calls)
			}
			var status interface{ StatusCode() int }
			if !errors.As(fmt.Errorf("wrapped: %w", err), &status) || status.StatusCode() != 400 {
				t.Errorf("lost refresh HTTP status: %v", err)
			}
		})
	}
}
func TestRefreshErrorsPreserveRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		stop   bool
	}{
		{400, `{"error":"invalid_grant"}`, true}, {401, `{"error":"invalid_grant"}`, true},
		{400, `{"error":"server_error","error_description":"text mentions invalid_grant"}`, false},
		{500, `{"error":"invalid_grant"}`, false}, {429, `{"error":"invalid_grant"}`, false}, {400, `not json invalid_grant`, false},
	} {
		t.Run(fmt.Sprintf("%d_%s", tc.status, tc.body), func(t *testing.T) {
			auth := &CodexAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}}
			_, err := auth.RefreshTokens(context.Background(), "fixture-policy-"+tc.body)
			if err == nil {
				t.Fatal("expected error")
			}
			if got := isNonRetryableRefreshErr(err); got != tc.stop {
				t.Errorf("stop=%v want %v", got, tc.stop)
			}
		})
	}
}
