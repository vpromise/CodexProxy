package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func intakeTestHandler(t *testing.T) *Handler {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	directory := filepath.Join(t.TempDir(), "auths")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: directory}, coreauth.NewManager(nil, nil, nil))
}

func intakeRequest(t *testing.T, handler gin.HandlerFunc, token, body, id string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest(http.MethodPost, "http://example.test/oauth-enroll/complete", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if token != "" {
		c.Request.Header.Set("Authorization", "Bearer "+token)
	}
	if id != "" {
		c.Params = gin.Params{{Key: "id", Value: id}}
	}
	handler(c)
	return response
}

func intakeInvite(t *testing.T, h *Handler, provider string) (string, *selfServiceRecord, string) {
	t.Helper()
	response := intakeRequest(t, h.CreateSelfServiceOAuth, "", `{"provider":"`+provider+`","label":"internal-only"}`, "")
	if response.Code != 201 {
		t.Fatalf("invite: %d %s", response.Code, response.Body)
	}
	var result struct {
		Record selfServiceSummary `json:"record"`
		Token  string             `json:"invitation_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	dir, err := h.selfServiceDir()
	if err != nil {
		t.Fatal(err)
	}
	r, err := selfServiceRead(dir, result.Record.ID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, r.ID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(strings.Split(result.Token, ".")[1])) {
		t.Fatal("raw invitation secret stored")
	}
	return result.Token, r, dir
}

func intakeStart(t *testing.T, h *Handler, token string, r *selfServiceRecord, dir string) *selfServiceRecord {
	t.Helper()
	response := intakeRequest(t, h.StartSelfServiceOAuth, token, `{"consent":true}`, "")
	if response.Code != 200 {
		t.Fatalf("start: %d %s", response.Code, response.Body)
	}
	r, err := selfServiceRead(dir, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if r.State == "" || r.Verifier == "" || r.AuthURL == "" || r.ConsentAt == nil {
		t.Fatal("missing server-side PKCE flow")
	}
	if strings.Contains(response.Body.String(), r.Verifier) {
		t.Fatal("PKCE verifier exposed")
	}
	return r
}

func intakeCompleteBody(r *selfServiceRecord) string {
	data, _ := json.Marshal(map[string]string{"callback": "test-code#" + r.State})
	return string(data)
}

func intakeMockExchange(_ context.Context, _ *config.Config, r *selfServiceRecord, code string) (map[string]any, error) {
	if code != "test-code" {
		return nil, errors.New("wrong code")
	}
	return map[string]any{"type": r.Provider, "access_token": "PRIVATE-ACCESS", "refresh_token": "PRIVATE-REFRESH", "id_token": "PRIVATE-ID", "email": "owner@example.test", "account_uuid": "claude-owner", "account_id": "codex-owner", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "claude_device_ids": []any{"retained-device"}}, nil
}

func TestDeleteExpiredInvitationPreservesInFlightAndImportedRecords(t *testing.T) {
	h := intakeTestHandler(t)
	for _, tc := range []struct {
		status      string
		file        string
		credentials map[string]any
		want        int
	}{
		{status: "invited", want: http.StatusOK},
		{status: "authorizing", want: http.StatusOK},
		{status: "exchanging", want: http.StatusConflict},
		{status: "pending", credentials: map[string]any{"type": "claude"}, want: http.StatusConflict},
		{status: "importing", want: http.StatusConflict},
		{status: "accepted", file: "account.json", want: http.StatusConflict},
		{status: "revoked", file: "account.json", want: http.StatusConflict},
		{status: "revoked", credentials: map[string]any{"type": "claude"}, want: http.StatusConflict},
	} {
		_, record, dir := intakeInvite(t, h, "claude")
		record.Status, record.FileName, record.Credentials = tc.status, tc.file, tc.credentials
		record.ExpiresAt = time.Now().Add(-time.Hour)
		if err := selfServiceWrite(dir, record); err != nil {
			t.Fatal(err)
		}
		response := intakeRequest(t, h.DeleteSelfServiceOAuth, "", "", record.ID)
		_, err := selfServiceRead(dir, record.ID)
		if response.Code != tc.want || (tc.want == http.StatusOK && !os.IsNotExist(err)) || (tc.want != http.StatusOK && err != nil) {
			t.Fatalf("%s deletion: HTTP %d, storage error %v", tc.status, response.Code, err)
		}
	}
}

func TestSelfServiceOAuthSubmissionAndDisabledAcceptance(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			h := intakeTestHandler(t)
			h.selfServiceExchange = intakeMockExchange
			token, r, dir := intakeInvite(t, h, provider)
			r = intakeStart(t, h, token, r, dir)
			// Recreating the handler proves that pending PKCE state survives a restart.
			restarted := NewHandlerWithoutConfigFilePath(h.cfg, h.authManager)
			restarted.selfServiceExchange = intakeMockExchange
			completed := intakeRequest(t, restarted.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), "")
			if completed.Code != 200 {
				t.Fatalf("complete: %d %s", completed.Code, completed.Body)
			}
			listing := intakeRequest(t, h.ListSelfServiceOAuth, "", `{}`, "")
			public := intakeRequest(t, h.GetSelfServiceOAuth, token, `{}`, "")
			for _, response := range []*httptest.ResponseRecorder{completed, listing, public} {
				for _, secret := range []string{"PRIVATE-ACCESS", "PRIVATE-REFRESH", "PRIVATE-ID", r.Verifier, r.State} {
					if strings.Contains(response.Body.String(), secret) {
						t.Fatalf("secret leaked: %s", response.Body)
					}
				}
			}
			if strings.Contains(public.Body.String(), "owner@example.test") || strings.Contains(public.Body.String(), "internal-only") {
				t.Fatal("public identity leak")
			}
			if len(h.authManager.List()) != 0 {
				t.Fatal("unreviewed credential registered")
			}
			entries, err := os.ReadDir(h.cfg.AuthDir)
			if err != nil || len(entries) != 0 {
				t.Fatal("pending credential entered auth directory")
			}
			record, err := selfServiceRead(dir, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if record.Credentials["refresh_token"] != "PRIVATE-REFRESH" || record.Verifier != "" {
				t.Fatal("pending credentials/flow not saved correctly")
			}
			for _, path := range []string{dir, filepath.Join(dir, r.ID+".json")} {
				st, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if st.Mode().Perm()&0o077 != 0 {
					t.Fatal("unsafe credential permissions")
				}
			}
			response := intakeRequest(t, h.AcceptSelfServiceOAuth, "", `{}`, r.ID)
			if response.Code != 200 {
				t.Fatalf("accept: %d %s", response.Code, response.Body)
			}
			auths := h.authManager.List()
			if len(auths) != 1 || !auths[0].Disabled || auths[0].Metadata["disabled"] != true {
				t.Fatal("accepted credential must remain disabled")
			}
			if len(h.cfg.AccessIsolation.Pools) != 0 {
				t.Fatal("pool changed during intake")
			}
			data, err := os.ReadFile(filepath.Join(h.cfg.AuthDir, auths[0].FileName))
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"PRIVATE-ACCESS", "PRIVATE-REFRESH", "PRIVATE-ID", "retained-device"} {
				if !bytes.Contains(data, []byte(value)) {
					t.Fatalf("provider metadata lost: %s", value)
				}
			}
			record, _ = selfServiceRead(dir, r.ID)
			if record.Status != "accepted" || record.Credentials != nil {
				t.Fatal("intake credentials not cleared after acceptance")
			}
			if retry := intakeRequest(t, h.AcceptSelfServiceOAuth, "", `{}`, r.ID); retry.Code != 200 || len(h.authManager.List()) != 1 {
				t.Fatal("acceptance not idempotent")
			}
			if replay := intakeRequest(t, h.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), ""); replay.Code != 409 {
				t.Fatal("completed invitation replayed")
			}
		})
	}
}

func TestSelfServiceOAuthValidationAndRevocation(t *testing.T) {
	h := intakeTestHandler(t)
	h.selfServiceExchange = intakeMockExchange
	token, r, dir := intakeInvite(t, h, "claude")
	if response := intakeRequest(t, h.StartSelfServiceOAuth, token, `{}`, ""); response.Code != 400 {
		t.Fatal("consent bypass")
	}
	for _, bad := range []string{"../secret." + strings.Repeat("a", 64), r.ID + "." + strings.Repeat("0", 64), "management-secret"} {
		if response := intakeRequest(t, h.GetSelfServiceOAuth, bad, `{}`, ""); response.Code != 401 {
			t.Fatal("invalid capability accepted")
		}
	}
	r = intakeStart(t, h, token, r, dir)
	for _, callback := range []string{"test-code#wrong-state", "http://evil.test/callback?code=test-code&state=" + r.State, "http://localhost:54545/callback?code=a&code=b&state=" + r.State} {
		data, _ := json.Marshal(map[string]string{"callback": callback})
		if response := intakeRequest(t, h.CompleteSelfServiceOAuth, token, string(data), ""); response.Code != 400 {
			t.Fatal("invalid callback accepted")
		}
	}
	revoked := intakeRequest(t, h.RejectSelfServiceOAuth, "", `{}`, r.ID)
	if revoked.Code != 200 {
		t.Fatal("revoke failed")
	}
	if response := intakeRequest(t, h.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), ""); response.Code != 401 {
		t.Fatal("revoked invitation accepted")
	}
	token, r, dir = intakeInvite(t, h, "codex")
	r.ExpiresAt = time.Now().Add(-time.Second)
	if err := selfServiceWrite(dir, r); err != nil {
		t.Fatal(err)
	}
	if response := intakeRequest(t, h.GetSelfServiceOAuth, token, `{}`, ""); response.Code != 401 {
		t.Fatal("expired capability accepted")
	}
}

func TestSelfServiceOAuthInterruptedExchangeAndCallbackFragments(t *testing.T) {
	h := intakeTestHandler(t)
	token, r, dir := intakeInvite(t, h, "claude")
	r = intakeStart(t, h, token, r, dir)
	if _, err := selfServiceCallback("http://localhost:54545/callback?code=a%23override&state="+r.State, r); err == nil {
		t.Fatal("provider could override verified state through code fragment")
	}
	r.Status = "exchanging"
	r.ExchangeAt = time.Now().Add(-3 * time.Minute)
	if err := selfServiceWrite(dir, r); err != nil {
		t.Fatal(err)
	}
	response := intakeRequest(t, h.GetSelfServiceOAuth, token, `{}`, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"failed"`) {
		t.Fatal("interrupted exchange waits indefinitely")
	}
	r, _ = selfServiceRead(dir, r.ID)
	if r.Verifier != "" || r.Status != "failed" {
		t.Fatal("interrupted flow not closed")
	}
}

func TestSelfServiceOAuthSingleExchangeAndRevokeRace(t *testing.T) {
	h := intakeTestHandler(t)
	token, r, dir := intakeInvite(t, h, "codex")
	r = intakeStart(t, h, token, r, dir)
	entered := make(chan struct{})
	release := make(chan struct{})
	var exchanges atomic.Int32
	h.selfServiceExchange = func(ctx context.Context, cfg *config.Config, r *selfServiceRecord, code string) (map[string]any, error) {
		exchanges.Add(1)
		close(entered)
		<-release
		return intakeMockExchange(ctx, cfg, r, code)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	var first *httptest.ResponseRecorder
	go func() {
		defer wg.Done()
		first = intakeRequest(t, h.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), "")
	}()
	<-entered
	retry := intakeRequest(t, h.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), "")
	if retry.Code != 409 || exchanges.Load() != 1 {
		t.Fatal("duplicate exchange")
	}
	if response := intakeRequest(t, h.RejectSelfServiceOAuth, "", `{}`, r.ID); response.Code != 200 {
		t.Fatal("revoke during exchange failed")
	}
	close(release)
	wg.Wait()
	if first.Code != 409 {
		t.Fatal("revocation lost race")
	}
	stored, _ := selfServiceRead(dir, r.ID)
	if stored.Credentials != nil || len(h.authManager.List()) != 0 {
		t.Fatal("revoked exchange saved credentials")
	}
}

func TestSelfServiceOAuthDuplicateAndPartialImport(t *testing.T) {
	h := intakeTestHandler(t)
	h.selfServiceExchange = intakeMockExchange
	token, r, dir := intakeInvite(t, h, "claude")
	r = intakeStart(t, h, token, r, dir)
	if response := intakeRequest(t, h.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	existing := filepath.Join(h.cfg.AuthDir, "existing.json")
	original := []byte(`{"type":"claude","account_uuid":"claude-owner","email":"owner@example.test","disabled":false,"access_token":"ORIGINAL"}`)
	if err := os.WriteFile(existing, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if response := intakeRequest(t, h.AcceptSelfServiceOAuth, "", `{}`, r.ID); response.Code != 409 {
		t.Fatal("duplicate accepted")
	}
	if after, _ := os.ReadFile(existing); !bytes.Equal(after, original) {
		t.Fatal("existing account overwritten")
	}
	if err := os.Remove(existing); err != nil {
		t.Fatal(err)
	}
	h.SetPostAuthPersistHook(func(context.Context, *coreauth.Auth) error { return errors.New("simulated hook failure") })
	if response := intakeRequest(t, h.AcceptSelfServiceOAuth, "", `{}`, r.ID); response.Code != 503 {
		t.Fatal("partial import not reported")
	}
	stored, _ := selfServiceRead(dir, r.ID)
	if stored.Status != "importing" {
		t.Fatal("lost resumable import")
	}
	path := filepath.Join(h.cfg.AuthDir, stored.FileName)
	var metadata map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	metadata["disabled"] = false
	metadata["access_token"] = "NEWER-TOKEN"
	data, _ = json.Marshal(metadata)
	if err = os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	h.SetPostAuthPersistHook(nil)
	if response := intakeRequest(t, h.AcceptSelfServiceOAuth, "", `{}`, r.ID); response.Code != 200 {
		t.Fatal(response.Body)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Contains(after, []byte("NEWER-TOKEN")) || bytes.Contains(after, []byte(`"disabled":true`)) {
		t.Fatal("retry overwrote later administrator changes")
	}
}

func TestSelfServiceOAuthOriginAndBodyBounds(t *testing.T) {
	h := intakeTestHandler(t)
	token, _, _ := intakeInvite(t, h, "claude")
	response := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(response)
	c.Request = httptest.NewRequest("POST", "http://example.test/oauth-enroll/start", strings.NewReader(`{"consent":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Authorization", "Bearer "+token)
	c.Request.Header.Set("Origin", "https://evil.test")
	h.StartSelfServiceOAuth(c)
	if response.Code != 403 {
		t.Fatal("cross-origin request accepted")
	}
	if response := intakeRequest(t, h.CompleteSelfServiceOAuth, token, `{"callback":"`+strings.Repeat("x", 9000)+`"}`, ""); response.Code != 400 {
		t.Fatal("oversized callback accepted")
	}
}

func TestSelfServiceOAuthFailureAndRejectionNeverActivateCredentials(t *testing.T) {
	h := intakeTestHandler(t)
	h.selfServiceExchange = func(context.Context, *config.Config, *selfServiceRecord, string) (map[string]any, error) {
		return nil, errors.New("upstream error with PRIVATE-REFRESH and PRIVATE-ACCESS")
	}
	token, r, dir := intakeInvite(t, h, "codex")
	r = intakeStart(t, h, token, r, dir)
	response := intakeRequest(t, h.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), "")
	if response.Code != 502 || strings.Contains(response.Body.String(), "PRIVATE") {
		t.Fatalf("unsafe exchange error: %d %s", response.Code, response.Body)
	}
	stored, _ := selfServiceRead(dir, r.ID)
	if stored.Status != "failed" || stored.Verifier != "" || stored.Credentials != nil {
		t.Fatal("failed exchange retained secret flow")
	}
	h.selfServiceExchange = intakeMockExchange
	token, r, dir = intakeInvite(t, h, "claude")
	r = intakeStart(t, h, token, r, dir)
	if response = intakeRequest(t, h.CompleteSelfServiceOAuth, token, intakeCompleteBody(r), ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response = intakeRequest(t, h.RejectSelfServiceOAuth, "", `{}`, r.ID); response.Code != 200 {
		t.Fatal(response.Body)
	}
	stored, _ = selfServiceRead(dir, r.ID)
	if stored.Status != "rejected" || stored.Credentials != nil || len(h.authManager.List()) != 0 {
		t.Fatal("rejected credential was retained or registered")
	}
	if response = intakeRequest(t, h.AcceptSelfServiceOAuth, "", `{}`, r.ID); response.Code != 409 {
		t.Fatal("rejected credential accepted")
	}
}
