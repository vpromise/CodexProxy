package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

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
