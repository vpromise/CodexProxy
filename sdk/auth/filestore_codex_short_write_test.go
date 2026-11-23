//go:build darwin || linux

package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStoreCodexShortWritePreservesCredential(t *testing.T) {
	const childEnv = "CLIPROXY_TEST_CODEX_SHORT_WRITE"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFileTokenStoreCodexShortWritePreservesCredential$", "-test.v")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, errRun := command.CombinedOutput(); errRun != nil {
			t.Fatalf("credential short-write subprocess: %v\n%s", errRun, output)
		}
		return
	}

	signal.Ignore(syscall.SIGXFSZ)
	t.Cleanup(func() { signal.Reset(syscall.SIGXFSZ) })
	for _, name := range []string{"relogin", "refresh", "first-login"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			store := NewFileTokenStore()
			store.SetBaseDir(dir)
			const fileName = "codex.json"
			path := filepath.Join(dir, fileName)
			old := []byte(`{"type":"codex","access_token":"synthetic-old-a","refresh_token":"synthetic-old-r","disabled":false}`)
			if name != "first-login" {
				if errWrite := os.WriteFile(path, old, 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
			}
			longToken := strings.Repeat("synthetic-fixture-", 64)
			var save func() error
			if name == "refresh" {
				manager := cliproxyauth.NewManager(store, nil, nil)
				if errLoad := manager.Load(ctx); errLoad != nil {
					t.Fatal(errLoad)
				}
				prior, ok := manager.GetByID(fileName)
				if !ok {
					t.Fatal("missing previously loaded credential")
				}
				next := prior.Clone()
				next.Metadata["refresh_token"] = longToken
				save = func() error {
					_, errUpdate := manager.UpdateRefreshedAuth(ctx, prior, next)
					current, ok := manager.GetByID(fileName)
					if !ok || current.Metadata["refresh_token"] != longToken {
						t.Error("refresh did not retain the new credential in memory")
					}
					return errUpdate
				}
			} else {
				record := &cliproxyauth.Auth{
					ID: fileName, Provider: "codex", FileName: fileName,
					Storage: &codex.CodexTokenStorage{Email: "test@example.invalid", RefreshToken: longToken},
				}
				save = func() error {
					_, errSave := store.Save(ctx, record)
					return errSave
				}
			}

			var original syscall.Rlimit
			if errLimit := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); errLimit != nil {
				t.Fatal(errLimit)
			}
			limited := original
			limited.Cur = 128
			if errLimit := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); errLimit != nil {
				t.Fatal(errLimit)
			}
			errSave := save()
			if errLimit := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); errLimit != nil {
				t.Fatal(errLimit)
			}
			if !errors.Is(errSave, syscall.EFBIG) {
				t.Fatalf("save error = %v, want file size limit error", errSave)
			}

			raw, errRead := os.ReadFile(path)
			if name == "first-login" {
				if !os.IsNotExist(errRead) {
					t.Fatalf("failed first login left a visible credential file (read error: %v, size: %d)", errRead, len(raw))
				}
			} else if errRead != nil || !bytes.Equal(old, raw) {
				t.Fatalf("failed write changed the previous credential (read error: %v, size: %d)", errRead, len(raw))
			}
			entries, errList := os.ReadDir(dir)
			expected := 1
			if name == "first-login" {
				expected = 0
			}
			if errList != nil || len(entries) != expected {
				t.Fatalf("unexpected files after failed save: count=%d, error=%v", len(entries), errList)
			}
		})
	}
}
