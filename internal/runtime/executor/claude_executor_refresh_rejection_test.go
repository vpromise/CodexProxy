package executor

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestClaudeAccessRejectionRequiresActualSelectedBearer(t *testing.T) {
	for _, tc := range []struct {
		name, url, method, bearer, host, alternateKey string
		status                                        int
		want                                          bool
	}{
		{name: "selected bearer", bearer: "fixture-access", status: http.StatusUnauthorized, want: true},
		{name: "alternate bearer", bearer: "other-token", status: http.StatusUnauthorized},
		{name: "alternate api key", bearer: "fixture-access", alternateKey: "other-key", status: http.StatusUnauthorized},
		{name: "custom upstream", url: "https://example.test/v1/messages", bearer: "fixture-access", status: http.StatusUnauthorized},
		{name: "insecure scheme", url: "http://api.anthropic.com/v1/messages", bearer: "fixture-access", status: http.StatusUnauthorized},
		{name: "count endpoint", url: "https://api.anthropic.com/v1/messages/count_tokens", bearer: "fixture-access", status: http.StatusUnauthorized},
		{name: "other method", method: http.MethodGet, bearer: "fixture-access", status: http.StatusUnauthorized},
		{name: "host override", bearer: "fixture-access", host: "example.test", status: http.StatusUnauthorized},
		{name: "other status", bearer: "fixture-access", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.url == "" {
				tc.url = "https://api.anthropic.com/v1/messages"
			}
			if tc.method == "" {
				tc.method = http.MethodPost
			}
			req := httptest.NewRequest(tc.method, tc.url, nil)
			req.Header.Set("Authorization", "Bearer "+tc.bearer)
			if tc.host != "" {
				req.Host = tc.host
			}
			if tc.alternateKey != "" {
				req.Header["x-api-key"] = []string{tc.alternateKey}
			}
			auth := &cliproxyauth.Auth{Provider: "claude", Metadata: map[string]any{"access_token": "fixture-access"}}
			response := &http.Response{StatusCode: tc.status, Request: req}
			var err error = statusErr{code: tc.status, msg: "fixture failure"}
			recordClaudeAccessTokenRejection(auth, response, &err)
			if got := claudeauth.IsAccessTokenRejection(err); got != tc.want {
				t.Fatalf("access rejection=%v, want %v", got, tc.want)
			}
			var original statusErr
			if !errors.As(err, &original) || original.StatusCode() != tc.status || original.Error() != "fixture failure" {
				t.Fatalf("native authentication outcome changed: %v", err)
			}
		})
	}
}
