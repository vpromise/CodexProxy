package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/fileperm"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRetryLocalCodexRefreshChecksFileAndDirectoryStage(t *testing.T) {
	for _, scenario := range []string{"sync-only", "replaced", "edited", "removed", "renamed"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "codex.json")
			const prior = `{"type":"codex","refresh_token":"r1"}`
			if errWrite := os.WriteFile(path, []byte(prior), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			expected, _, errRead := readCodexFileVersion(path)
			if errRead != nil {
				t.Fatal(errRead)
			}
			version := prior
			switch scenario {
			case "edited":
				version = `{"type":"codex","refresh_token":"changed"}`
				if errWrite := os.WriteFile(path, []byte(version), 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
			case "replaced", "renamed":
				if errRename := os.Rename(path, path+".old"); errRename != nil {
					t.Fatal(errRename)
				}
				if scenario == "replaced" {
					// The bytes alone cannot distinguish a new file with the same contents.
					if errWrite := os.WriteFile(path, []byte(version), 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
				}
			case "removed":
				if errRemove := os.Remove(path); errRemove != nil {
					t.Fatal(errRemove)
				}
			}

			syncOnly := scenario == "sync-only"
			retry := NewFileTokenStore().retryLocalCodexRefresh(path, expected, syncOnly)
			done, errRetry := retry(t.Context(), &cliproxyauth.Auth{Metadata: map[string]any{
				"type": "codex", "refresh_token": "must-not-be-written",
			}})
			if errRetry != nil || done != syncOnly {
				t.Fatalf("recovery = %t, %v", done, errRetry)
			}
			current, raw, errRead := readCodexFileVersion(path)
			if scenario == "removed" || scenario == "renamed" {
				if !errors.Is(errRead, os.ErrNotExist) {
					t.Fatalf("original path was recreated: %v", errRead)
				}
			} else {
				if errRead != nil || !bytes.Equal(raw, []byte(version)) {
					t.Fatalf("file changed unexpectedly: %v", errRead)
				}
				if syncOnly && !os.SameFile(current.info, expected.info) {
					t.Fatal("directory-only recovery replaced the credential inode")
				}
			}
		})
	}
}

func TestFileTokenStoreCodexRefreshDoesNotOverwriteUnobservedRelogin(t *testing.T) {
	dir := t.TempDir()
	const name = "codex.json"
	path := filepath.Join(dir, name)
	const old = `{"type":"codex","access_token":"a1","refresh_token":"r1"}`
	if errWrite := os.WriteFile(path, []byte(old), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := cliproxyauth.NewManager(store, nil, nil)
	if errLoad := manager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	started, ok := manager.GetByID(name)
	if !ok {
		t.Fatal("credential did not load")
	}
	const relogin = `{"type":"codex","access_token":"a3","refresh_token":"r3"}`
	if errWrite := os.WriteFile(path, []byte(relogin), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	updated := started.Clone()
	updated.Metadata["access_token"] = "a2"
	updated.Metadata["refresh_token"] = "r2"
	_, errSave := manager.UpdateRefreshedAuth(t.Context(), started, updated)
	if !errors.Is(errSave, fileperm.ErrCredentialFileChanged) {
		t.Fatalf("refresh save = %v, want source-changed error", errSave)
	}
	if raw, errRead := os.ReadFile(path); errRead != nil || !bytes.Equal(raw, []byte(relogin)) {
		t.Fatalf("relogin file was overwritten: %v", errRead)
	}
}
