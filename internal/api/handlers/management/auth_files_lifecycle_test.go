package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type managementExpiredClaudeExecutor struct{ coreauth.ProviderExecutor }

func (managementExpiredClaudeExecutor) Identifier() string { return "claude" }

func (managementExpiredClaudeExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, &claudeauth.RefreshTokenExpiredError{}
}

func TestAuthFilesClaudeSignInNoticeDoesNotInvalidateAccessOrAllowManualExpiry(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(managementExpiredClaudeExecutor{})
	ctx := t.Context()
	dir := t.TempDir()
	const file = "claude-expired-refresh.json"
	expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	a := &coreauth.Auth{
		ID: file, FileName: file, Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"path": filepath.Join(dir, file)},
		Metadata: map[string]any{
			"access_token": "fixture-access", "refresh_token": "fixture-refresh",
			"expired": expiry, "refresh_interval_seconds": 1,
		},
	}
	if _, errRegister := manager.Register(ctx, a); errRegister != nil {
		t.Fatal(errRegister)
	}
	manager.StartAutoRefresh(ctx, time.Millisecond)
	t.Cleanup(manager.StopAutoRefresh)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	current, _ := manager.GetByID(file)
	for !current.RequiresSignIn() {
		select {
		case <-deadline.C:
			t.Fatal("background refresh did not record sign-in state")
		case <-ticker.C:
			current, _ = manager.GetByID(file)
		}
	}
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, manager)
	authEntry := handler.buildAuthFileEntry(current)
	if authEntry["requires_sign_in"] != true || authEntry["unavailable"] != false || authEntry["status_message"] != "" {
		t.Fatalf("management warning changed access availability or status: %+v", authEntry)
	}

	response := httptest.NewRecorder()
	request, _ := gin.CreateTestContext(response)
	request.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields",
		strings.NewReader(`{"name":"claude-expired-refresh.json","expired":"1999-01-01T00:00:00Z"}`))
	handler.PatchAuthFileFields(request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), `"requires_sign_in"`) {
		t.Fatalf("manual refresh response=%d %s", response.Code, response.Body.String())
	}
	current, _ = manager.GetByID(file)
	if current.Metadata["expired"] != expiry || !current.HasValidAccessToken(time.Now()) {
		t.Fatal("manual refresh invalidated the still-valid access token")
	}
}

type lifecycleBlockingFileStore struct {
	*fileauth.FileTokenStore
	block   atomic.Bool
	started chan struct{}
	release chan struct{}
	deleted chan struct{}
}

func (s *lifecycleBlockingFileStore) Save(ctx context.Context, auth *coreauth.Auth) (string, error) {
	if s.block.Swap(false) {
		close(s.started)
		<-s.release
	}
	return s.FileTokenStore.Save(ctx, auth)
}

func (s *lifecycleBlockingFileStore) Delete(ctx context.Context, id string) error {
	close(s.deleted)
	return s.FileTokenStore.Delete(ctx, id)
}

func TestDeleteAuthFile_DrainsOlderSaveBeforeDeleting(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	fileStore := fileauth.NewFileTokenStore()
	fileStore.SetBaseDir(dir)
	store := &lifecycleBlockingFileStore{FileTokenStore: fileStore, started: make(chan struct{}), release: make(chan struct{}), deleted: make(chan struct{})}
	m := coreauth.NewManager(store, nil, nil)
	a, err := m.Register(ctx, &coreauth.Auth{ID: "claude-lifecycle.json", FileName: "claude-lifecycle.json", Provider: "claude", Status: coreauth.StatusActive,
		Attributes: map[string]string{"path": filepath.Join(dir, "claude-lifecycle.json")}, Metadata: map[string]any{"type": "claude", "access_token": "initial"}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: dir}, m)
	h.tokenStore = store
	store.block.Store(true)
	a.Metadata["access_token"] = "rotated"
	saveDone := make(chan error, 1)
	go func() { _, errUpdate := m.Update(ctx, a); saveDone <- errUpdate }()
	<-store.started
	recorder := httptest.NewRecorder()
	deleteCtx, _ := gin.CreateTestContext(recorder)
	deleteCtx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/auth-files?name=claude-lifecycle.json", nil)
	deleteDone := make(chan struct{})
	go func() { h.DeleteAuthFile(deleteCtx); close(deleteDone) }()
	select {
	case <-store.deleted:
		close(store.release)
		<-saveDone
		<-deleteDone
		t.Fatal("delete reached storage before the earlier writer completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(store.release)
	if errSave := <-saveDone; errSave != nil {
		t.Fatal(errSave)
	}
	<-deleteDone
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", recorder.Code, recorder.Body.String())
	}
	if _, errStat := os.Stat(filepath.Join(dir, a.FileName)); !os.IsNotExist(errStat) {
		t.Fatalf("old save recreated deleted file: %v", errStat)
	}
	if _, exists := m.GetByID(a.ID); exists {
		t.Fatal("deleted auth remains registered")
	}
}
