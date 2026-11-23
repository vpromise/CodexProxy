package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStoreCodexReadersSeeCompleteCredentials(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test requires Unix rename semantics while readers hold the file open")
	}
	ctx := context.Background()
	store, path, seeded := seedCodexFile(t)
	manager := cliproxyauth.NewManager(store, nil, nil)
	if errLoad := manager.Load(ctx); errLoad != nil {
		t.Fatal(errLoad)
	}
	current, ok := manager.GetByID(seeded.ID)
	if !ok {
		t.Fatal("credential was not loaded")
	}
	stop := make(chan struct{})
	readerDone := make(chan error, 1)
	readerReady := make(chan struct{})
	go func() {
		close(readerReady)
		for {
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				readerDone <- errRead
				return
			}
			if !json.Valid(data) {
				readerDone <- fmt.Errorf("read incomplete credential JSON of %d bytes", len(data))
				return
			}
		}
	}()
	defer func() {
		close(stop)
		if errRead := <-readerDone; errRead != nil {
			t.Error(errRead)
		}
	}()
	<-readerReady

	for generation := 1; generation <= 20; generation++ {
		next := current.Clone()
		next.Metadata["refresh_token"] = fmt.Sprintf("fixture-%d-%s", generation, strings.Repeat("x", 8192))
		var errUpdate error
		current, errUpdate = manager.UpdateRefreshedAuth(ctx, current, next)
		if errUpdate != nil || current == nil {
			t.Fatalf("save refreshed credential: %v", errUpdate)
		}
	}
	reloaded, errList := store.List(ctx)
	if errList != nil || len(reloaded) != 1 {
		t.Fatalf("credential reload: entries=%d, error=%v", len(reloaded), errList)
	}
	if token, _ := reloaded[0].Metadata["refresh_token"].(string); !strings.HasPrefix(token, "fixture-20-") {
		t.Fatal("reload did not read the latest refresh result")
	}
	if reloaded[0].Metadata["note"] != "fixture-note" {
		t.Fatal("refresh did not preserve custom metadata")
	}
	info, errStat := os.Stat(path)
	if errStat != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected credential file permissions: %v, %v", info, errStat)
	}
	if _, errSave := manager.UpdateRefreshedAuth(ctx, current, current.Clone()); errSave != nil {
		t.Fatalf("repeat the same save: %v", errSave)
	}
	after, errStat := os.Stat(path)
	if errStat != nil || !os.SameFile(info, after) {
		t.Fatalf("saving the same credential replaced its file: %v", errStat)
	}
}

func TestFileTokenStoreCodexStaleRefreshKeepsDisabledAndRemoved(t *testing.T) {
	ctx := context.Background()
	store, path, record := seedCodexFile(t)
	manager := cliproxyauth.NewManager(store, nil, nil)
	if errLoad := manager.Load(ctx); errLoad != nil {
		t.Fatal(errLoad)
	}
	prior, ok := manager.GetByID(record.ID)
	if !ok {
		t.Fatal("credential was not loaded")
	}
	pending := prior.Clone()
	pending.Metadata["refresh_token"] = "fixture-refresh"
	if _, errDisable := manager.PatchAuth(ctx, prior.ID, prior.RegistrationEpoch, func(current *cliproxyauth.Auth) error {
		current.Disabled = true
		current.Status = cliproxyauth.StatusDisabled
		return nil
	}); errDisable != nil {
		t.Fatal(errDisable)
	}

	if _, errUpdate := manager.UpdateRefreshedAuth(ctx, prior, pending); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	reloaded, errList := store.List(ctx)
	if errList != nil || len(reloaded) != 1 || !reloaded[0].Disabled {
		t.Fatalf("reload did not retain the disabled credential: count=%d, error=%v", len(reloaded), errList)
	}

	deleteFile := func() error { return store.Delete(ctx, prior.ID) }
	if errRemove := manager.RemoveWithPersistence(ctx, prior.ID, deleteFile, prior.RegistrationEpoch); errRemove != nil {
		t.Fatal(errRemove)
	}
	if updated, _ := manager.UpdateRefreshedAuth(ctx, prior, pending); updated != nil {
		t.Fatal("a stale manager update registered the removed credential")
	}
	if _, errSave := store.Save(ctx, pending); !errors.Is(errSave, os.ErrNotExist) {
		t.Fatalf("old file-backed snapshot save returned %v, want a missing source", errSave)
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("removed credential reappeared: %v", errStat)
	}
}

func seedCodexFile(t *testing.T) (*FileTokenStore, string, *cliproxyauth.Auth) {
	t.Helper()
	dir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	const name = "codex.json"
	record := &cliproxyauth.Auth{
		ID: name, FileName: name, Provider: "codex",
		Metadata: map[string]any{"type": "codex", "refresh_token": "fixture-seed", "note": "fixture-note"},
	}
	path, errSave := store.Save(context.Background(), record)
	if errSave != nil || path != filepath.Join(dir, name) {
		t.Fatalf("seed Codex file: %q, %v", path, errSave)
	}
	return store, path, record
}
