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
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStoreRecoversCodexRefreshWithoutAnotherExchange(t *testing.T) {
	const childEnv = "CLIPROXY_TEST_CODEX_DISK_RECOVERY"
	if os.Getenv(childEnv) != "1" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestFileTokenStoreRecoversCodexRefreshWithoutAnotherExchange$", "-test.v")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, errRun := command.CombinedOutput(); errRun != nil {
			t.Fatalf("credential recovery subprocess: %v\n%s", errRun, output)
		}
		return
	}

	signal.Ignore(syscall.SIGXFSZ)
	t.Cleanup(func() { signal.Reset(syscall.SIGXFSZ) })
	dir := t.TempDir()
	const name = "codex.json"
	path := filepath.Join(dir, name)
	old := []byte(`{"type":"codex","access_token":"old-access","refresh_token":"old-refresh","expired":"2000-01-01T00:00:00Z","refresh_interval":"1h"}`)
	if errWrite := os.WriteFile(path, old, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	hook := &diskRecoveryTestHook{completed: make(chan struct{}, 1)}
	manager := cliproxyauth.NewManager(store, nil, hook)
	if errLoad := manager.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	executor := &diskRecoveryTestExecutor{next: strings.Repeat("synthetic-rotated-", 64)}
	manager.RegisterExecutor(executor)

	var original syscall.Rlimit
	if errLimit := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &original); errLimit != nil {
		t.Fatal(errLimit)
	}
	t.Cleanup(func() {
		if errLimit := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); errLimit != nil {
			t.Error(errLimit)
		}
	})
	limited := original
	limited.Cur = 256
	if errLimit := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limited); errLimit != nil {
		t.Fatal(errLimit)
	}
	manager.StartAutoRefresh(t.Context(), time.Minute)
	defer manager.StopAutoRefresh()
	select {
	case <-hook.completed:
	case <-time.After(3 * time.Second):
		t.Fatal("initial synthetic refresh never completed")
	}
	current, ok := manager.GetByID(name)
	if !ok || current.Metadata["refresh_token"] != executor.next {
		t.Fatal("new refresh result is missing in memory")
	}
	if saved, errRead := os.ReadFile(path); errRead != nil || !bytes.Equal(saved, old) {
		t.Fatalf("failed first save did not retain previous JSON: %v", errRead)
	}
	if errLimit := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original); errLimit != nil {
		t.Fatal(errLimit)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		saved, errRead := os.ReadFile(path)
		if errRead == nil && bytes.Contains(saved, []byte(executor.next)) {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	manager.StopAutoRefresh()
	restarted := cliproxyauth.NewManager(store, nil, nil)
	if errLoad := restarted.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	record, ok := restarted.GetByID(name)
	if !ok || record.Metadata["refresh_token"] != executor.next {
		t.Fatal("the restarted manager could not load the rotated credential")
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("upstream refresh calls = %d, want one", got)
	}
}

type diskRecoveryTestHook struct {
	cliproxyauth.NoopHook
	completed chan struct{}
}

func (h *diskRecoveryTestHook) OnAuthUpdated(context.Context, *cliproxyauth.Auth) {
	select {
	case h.completed <- struct{}{}:
	default:
	}
}

type diskRecoveryTestExecutor struct {
	cliproxyauth.ProviderExecutor
	next  string
	calls atomic.Int32
}

func (*diskRecoveryTestExecutor) Identifier() string { return "codex" }

func (e *diskRecoveryTestExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	e.calls.Add(1)
	auth.Metadata["access_token"] = "rotated-access"
	auth.Metadata["refresh_token"] = e.next
	auth.Metadata["expired"] = time.Now().Add(14 * 24 * time.Hour).Format(time.RFC3339)
	return auth, nil
}

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
