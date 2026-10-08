package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeRefreshDoesNotReplayAmbiguousOutcome(t *testing.T) {
	for _, kind := range []string{"transport", "body-read", "decode", "invalid-grant"} {
		t.Run(kind, func(t *testing.T) {
			resetClaudeRefreshState()
			defer resetClaudeRefreshState()
			calls := 0
			auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if kind == "transport" {
					return nil, errors.New("connection reset after write")
				}
				response := jsonResponse(req, `{"access_token":`)
				if kind == "body-read" {
					response.Body = io.NopCloser(refreshBrokenReader{})
				}
				if kind == "invalid-grant" {
					response.StatusCode = 400
					response.Body = io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`))
				}
				return response, nil
			})}}
			_, err := auth.RefreshTokensWithRetry(t.Context(), "fixture-"+kind, 3)
			if err == nil || calls != 1 || !strings.Contains(err.Error(), "after 1 attempts") {
				t.Fatalf("calls=%d error=%v, want one failed attempt", calls, err)
			}
		})
	}
}

type refreshBrokenReader struct{}

func (refreshBrokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestClaudeRefreshRetriesExplicitServerFailure(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()
	calls := 0
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != RefreshTokenURL {
			return jsonResponse(req, `{"account":{"uuid":"account"},"organization":{"uuid":"org"}}`), nil
		}
		calls++
		if calls == 1 {
			resp := jsonResponse(req, `{"error":"temporarily_unavailable"}`)
			resp.StatusCode = http.StatusServiceUnavailable
			return resp, nil
		}
		return jsonResponse(req, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`), nil
	})}}
	token, err := auth.RefreshTokensWithRetry(t.Context(), "fixture-server-failure", 3)
	if err != nil || calls != 2 || token == nil || token.AccessToken != "new-access" {
		t.Fatalf("calls=%d token=%v error=%v", calls, token, err)
	}
}

func TestClaudeRefreshCancellationStopsRetry(t *testing.T) {
	resetClaudeRefreshState()
	defer resetClaudeRefreshState()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		response := jsonResponse(req, `{"error":"temporarily_unavailable"}`)
		response.StatusCode = http.StatusServiceUnavailable
		return response, nil
	})}}
	_, err := auth.RefreshTokensWithRetry(ctx, "fixture-cancel", 3)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}
