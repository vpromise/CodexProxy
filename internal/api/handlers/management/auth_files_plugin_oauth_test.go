package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestPluginLoginPollAuthsExpandsMultipleAuths(t *testing.T) {
	host := pluginhost.New()
	resp := pluginapi.AuthLoginPollResponse{
		Status: pluginapi.AuthLoginStatusSuccess,
		Auths: []pluginapi.AuthData{
			{
				Provider:    "custom-oauth",
				ID:          "customoauth.json",
				FileName:    "customoauth.json",
				StorageJSON: []byte(`{"type":"custom-oauth"}`),
			},
			{
				Provider:    "custom-oauth",
				ID:          "customoauth-project-a.json",
				FileName:    "customoauth-project-a.json",
				StorageJSON: []byte(`{"type":"custom-oauth","project_id":"project-a"}`),
				Metadata:    map[string]any{"project_id": "project-a"},
			},
		},
	}

	records := pluginLoginPollAuths(host, resp)
	if len(records) != 2 {
		t.Fatalf("pluginLoginPollAuths() len = %d, want two records", len(records))
	}
	if records[0].ID != "customoauth.json" || records[1].ID != "customoauth-project-a.json" {
		t.Fatalf("records = %#v, want both plugin auths", records)
	}
	if gotProject := records[1].Metadata["project_id"]; gotProject != "project-a" {
		t.Fatalf("project_id = %#v, want project-a", gotProject)
	}
}

func TestSavePluginLoginRecordsRollsBackSavedAuthsOnFailure(t *testing.T) {
	store := &pluginLoginRollbackStore{failAt: 2}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.tokenStore = store

	records := []*coreauth.Auth{
		{
			ID:       "customoauth.json",
			FileName: "customoauth.json",
			Provider: "custom-oauth",
			Metadata: map[string]any{"type": "custom-oauth"},
		},
		{
			ID:       "customoauth-project-a.json",
			FileName: "customoauth-project-a.json",
			Provider: "custom-oauth",
			Metadata: map[string]any{"type": "custom-oauth", "project_id": "project-a"},
		},
	}

	errSave := h.savePluginLoginRecords(context.Background(), records)
	if errSave == nil {
		t.Fatal("savePluginLoginRecords() error = nil, want rollback-triggering error")
	}
	if len(store.saved) != 2 {
		t.Fatalf("saved len = %d, want two attempted saves", len(store.saved))
	}
	if !store.deleted["customoauth.json"] || !store.deleted["customoauth-project-a.json"] {
		t.Fatalf("deleted = %#v, want both saved auths rolled back", store.deleted)
	}
}

func TestPatchPluginVirtualAuthStatusReturnsConflictForVirtualChild(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := pluginVirtualAuthForTest(t.TempDir(), "source.json", "auth-1")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register virtual auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"auth-1","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestPatchPluginVirtualSourceStatusDisablesAllExpandedAuths(t *testing.T) {
	authDir := t.TempDir()
	fileName := "source.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"custom-oauth","disabled":false}`), 0o600); errWrite != nil {
		t.Fatalf("write source auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"source.json", "virtual-project-a"} {
		auth := pluginVirtualAuthForTest(authDir, fileName, id)
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register virtual auth %s: %v", id, errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(`{"name":"source.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("read source auth file: %v", errRead)
	}
	if !strings.Contains(string(raw), `"disabled":true`) {
		t.Fatalf("source auth file = %s, want disabled:true", string(raw))
	}
	for _, id := range []string{"source.json", "virtual-project-a"} {
		auth, ok := manager.GetByID(id)
		if !ok || auth == nil {
			t.Fatalf("expected auth %s to remain registered", id)
		}
		if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
			t.Fatalf("auth %s disabled/status = %v/%s, want disabled", id, auth.Disabled, auth.Status)
		}
	}
}

func TestPatchPluginVirtualAuthFieldsReturnsConflict(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := pluginVirtualAuthForTest(t.TempDir(), "source.json", "auth-1")
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register virtual auth: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(`{"name":"auth-1","note":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileFields(ctx)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestDeletePluginVirtualSourceRemovesExpandedRuntimeAuths(t *testing.T) {
	authDir := t.TempDir()
	fileName := "source.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"custom-oauth"}`), 0o600); errWrite != nil {
		t.Fatalf("write source auth file: %v", errWrite)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"auth-1", "auth-2"} {
		auth := pluginVirtualAuthForTest(authDir, fileName, id)
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register virtual auth %s: %v", id, errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name="+url.QueryEscape(fileName), nil)
	ctx.Request = req

	h.DeleteAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, errStat := os.Stat(filePath); !os.IsNotExist(errStat) {
		t.Fatalf("expected source auth file to be removed, stat err: %v", errStat)
	}
	for _, id := range []string{"auth-1", "auth-2"} {
		if _, ok := manager.GetByID(id); ok {
			t.Fatalf("expected virtual auth %s to be removed", id)
		}
	}
}

func pluginVirtualAuthForTest(authDir, fileName, id string) *coreauth.Auth {
	filePath := filepath.Join(authDir, fileName)
	auth := &coreauth.Auth{
		ID:       id,
		FileName: fileName,
		Provider: "custom-oauth",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"type": "custom-oauth",
		},
	}
	coreauth.MarkPluginVirtualAuth(auth, filePath, 0)
	return auth
}

type pluginLoginRollbackStore struct {
	failAt  int
	saved   []string
	deleted map[string]bool
}

func (s *pluginLoginRollbackStore) List(context.Context) ([]*coreauth.Auth, error) {
	return nil, nil
}

func (s *pluginLoginRollbackStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	path := strings.TrimSpace(auth.FileName)
	if path == "" {
		path = strings.TrimSpace(auth.ID)
	}
	s.saved = append(s.saved, path)
	if len(s.saved) == s.failAt {
		return path, errors.New("save failed after write")
	}
	return path, nil
}

func (s *pluginLoginRollbackStore) Delete(_ context.Context, id string) error {
	if s.deleted == nil {
		s.deleted = make(map[string]bool)
	}
	s.deleted[id] = true
	return nil
}

func (s *pluginLoginRollbackStore) SetBaseDir(string) {}
