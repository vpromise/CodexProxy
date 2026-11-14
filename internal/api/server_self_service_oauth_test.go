package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelfServiceOAuthRoutesKeepManagementAndInferencePrivate(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	server := newTestServer(t)
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		server.engine.ServeHTTP(response, req)
		return response
	}
	for _, path := range []string{"/oauth-enroll", "/oauth-enroll/assets/app.js", "/oauth-enroll/assets/style.css"} {
		r := request("GET", path, "", "")
		if r.Code != 200 || r.Header().Get("Cache-Control") != "no-store" || r.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("public asset %s: %d %s", path, r.Code, r.Body)
		}
	}
	if r := request("GET", "/oauth-enroll/session", "", ""); r.Code != 401 {
		t.Fatalf("missing invitation: %d", r.Code)
	}
	r := request("POST", "/v0/management/oauth-intake", "test-management-key", `{"provider":"claude"}`)
	if r.Code != 201 {
		t.Fatalf("create: %d %s", r.Code, r.Body)
	}
	var invite struct {
		Token string `json:"invitation_token"`
	}
	if err := json.Unmarshal(r.Body.Bytes(), &invite); err != nil || invite.Token == "" {
		t.Fatal("missing invitation")
	}
	if r = request("GET", "/oauth-enroll/session", invite.Token, ""); r.Code != 200 || strings.Contains(r.Body.String(), "X-CPA") {
		t.Fatalf("public session: %d %s", r.Code, r.Body)
	}
	for _, path := range []string{"/v0/management/oauth-intake", "/v0/management/auth-files", "/v0/management/config", "/v1/models"} {
		r = request("GET", path, invite.Token, "")
		if r.Code != 401 && r.Code != 403 {
			t.Fatalf("invitation grants excess access to %s: %d", path, r.Code)
		}
	}
	// A platform inference key is not an enrollment capability either.
	if r = request("POST", "/oauth-enroll/start", "test-key", `{"consent":true}`); r.Code != 401 {
		t.Fatalf("inference key accepted: %d", r.Code)
	}
	if r = request("POST", "/oauth-enroll/start", invite.Token, `{"consent":true}`); r.Code != 200 {
		t.Fatalf("invited start: %d %s", r.Code, r.Body)
	}
	// Existing login endpoints remain behind the original management middleware.
	if r = request("GET", "/v0/management/anthropic-auth-url", "", ""); r.Code != 401 {
		t.Fatalf("existing OAuth permission changed: %d", r.Code)
	}
	if r = request("GET", "/v1/models", "test-key", ""); r.Code != 200 {
		t.Fatalf("existing inference permission changed: %d", r.Code)
	}
	server.managementRoutesEnabled.Store(false)
	if r = request("GET", "/oauth-enroll", "", ""); r.Code != 404 {
		t.Fatalf("disabled management exposes enrollment: %d", r.Code)
	}
}
