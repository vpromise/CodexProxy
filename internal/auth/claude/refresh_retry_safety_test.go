package claude

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestClaudeRefreshClassifiesOnlyExplicitExpiredGrant(t *testing.T) {
	const expiredGrant = `{"error":"invalid_grant","error_description":"Refresh token expired"}`
	for _, tc := range []struct {
		name       string
		body       string
		status     int
		otherRoute bool
		want       bool
	}{
		{name: "expired", body: expiredGrant, status: http.StatusBadRequest, want: true},
		{name: "generic", body: `{"error":"invalid_grant","error_description":"Bad request"}`, status: http.StatusBadRequest},
		{name: "other text", body: `{"error":"invalid_grant","message":"Refresh token expired"}`, status: http.StatusBadRequest},
		{name: "different grant", body: `{"error":"invalid_request","error_description":"Refresh token expired"}`, status: http.StatusBadRequest},
		{name: "other status", body: expiredGrant, status: http.StatusUnauthorized},
		{name: "other route", body: expiredGrant, status: http.StatusBadRequest, otherRoute: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			auth := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				resp := jsonResponse(req, tc.body)
				resp.StatusCode = tc.status
				if tc.otherRoute {
					resp.Request = req.Clone(req.Context())
					resp.Request.URL = &url.URL{Scheme: "https", Host: "example.test", Path: "/v1/oauth/token"}
				}
				return resp, nil
			})}}
			_, err := auth.RefreshTokensWithRetry(t.Context(), "expired-refresh-fixture-"+tc.name, 3)
			var signIn *RefreshTokenExpiredError
			if calls != 1 || err == nil || errors.As(err, &signIn) != tc.want {
				t.Fatalf("calls=%d classified=%v err=%v, want one attempt with expiry=%v", calls, signIn != nil, err, tc.want)
			}
			if tc.want && strings.Contains(err.Error(), "error_description") {
				t.Fatalf("typed error carried the raw response body: %v", err)
			}
		})
	}
}

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
