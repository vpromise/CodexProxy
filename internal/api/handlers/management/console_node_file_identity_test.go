package management

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestConsoleNodeAssignWithRuntimeFileID(t *testing.T) {
	h, server, cert := nodeTestServer(t)
	id := strings.Repeat("a", 32)
	p := h.cfg.ConsoleNode.EgressProfiles[0]
	record := &selfServiceRecord{
		ID: id, Provider: "claude", Status: "pending", AccountID: "synthetic-account",
		ExpiresAt: time.Now().Add(time.Hour),
		Console: &coreauth.ConsoleBinding{
			NodeID: h.cfg.ConsoleNode.NodeID, InstanceID: h.cfg.ConsoleNode.InstanceID,
			ProfileID: p.ID, ProfileVersion: p.Version, ProxyURL: p.ProxyURL,
		},
		Credentials: map[string]any{
			"type": "claude", "account_uuid": "synthetic-account",
			"organization_uuid": "synthetic-org", "expired": time.Now().Add(time.Hour).Format(time.RFC3339),
			"access_token": "synthetic-token", "refresh_token": "synthetic-refresh",
		},
	}
	dir, err := h.selfServiceDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := selfServiceWrite(dir, record); err != nil {
		t.Fatal(err)
	}
	h.SetPostAuthPersistHook(func(ctx context.Context, auth *coreauth.Auth) error {
		_, err := h.authManager.PatchAuth(coreauth.WithSkipPersist(ctx), auth.ID, auth.RegistrationEpoch, func(a *coreauth.Auth) error {
			a.FileName = ""
			return nil
		})
		return err
	})
	op := strings.Repeat("b", 32)
	code, receipt := nodeCall(t, server, cert, http.MethodPost, "/v1/enrollments/"+id+"/assign", gin.H{"operation_id": op, "pool_id": "one"}, "")
	if code != 200 {
		t.Errorf("accept and assign returned %d, receipt=%v", code, receipt["operation"])
	}
	_, inv := nodeCall(t, server, cert, http.MethodGet, "/v1/inventory", nil, "")
	if accounts := inv["accounts"].([]any); len(accounts) != 1 || accounts[0].(map[string]any)["enrollment_id"] != id {
		t.Fatalf("fixture account was not loaded in inventory")
	}
	_, data := nodeCall(t, server, cert, http.MethodGet, "/v1/enrollments/"+id, nil, "")
	if data["status"] != "accepted" || data["principal"] != "synthetic-account" {
		t.Errorf("loaded enrollment read back as %q, principal available=%v", data["status"], data["principal"] != nil)
	}
	if account, ok := data["account"].(map[string]any); !ok || account["disabled"] != true || len(account["pools"].([]any)) != 1 {
		t.Error("accepted account was not readable as assigned and still disabled")
	}
	a := h.authManager.List()[0]
	if a.FileName != "" || a.ID != "self-service-claude-"+id+".json" || a.Attributes["path"] != filepath.Join(h.cfg.AuthDir, a.ID) {
		t.Fatal("fixture did not use the runtime ID and exact source file path")
	}
	for _, tc := range []struct {
		name   string
		mutate func(*coreauth.Auth)
	}{
		{"different file path", func(a *coreauth.Auth) { a.Attributes["path"] = filepath.Join(h.cfg.AuthDir, "other.json") }},
		{"different invitation", func(a *coreauth.Auth) { a.Metadata["self_service_id"] = strings.Repeat("c", 32) }},
		{"missing node binding", func(a *coreauth.Auth) { delete(a.Metadata, "console_binding") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			update := func(mutate func(*coreauth.Auth)) {
				t.Helper()
				if _, err := h.authManager.PatchAuth(coreauth.WithSkipPersist(context.Background()), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
					fixture := a.Clone()
					current.Attributes, current.Metadata = fixture.Attributes, fixture.Metadata
					mutate(current)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			update(tc.mutate)
			_, data := nodeCall(t, server, cert, http.MethodGet, "/v1/enrollments/"+id, nil, "")
			if data["status"] != "unavailable" || data["principal"] != nil || data["account"] != nil {
				t.Fatal("unmatched credential was bound to the invitation")
			}
		})
	}
}
