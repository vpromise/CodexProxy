package watcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConfigWatcherSurvivesAtomicReplacement(t *testing.T) {
	for _, sharedDirectory := range []bool{false, true} {
		t.Run(fmt.Sprint(sharedDirectory), func(t *testing.T) {
			dir := t.TempDir()
			authDir := dir
			if !sharedDirectory {
				authDir = filepath.Join(dir, "auth")
				if errMkdir := os.Mkdir(authDir, 0o700); errMkdir != nil {
					t.Fatal(errMkdir)
				}
			}
			configPath := filepath.Join(dir, "config.yaml")
			payload := func(port int) []byte {
				return []byte(fmt.Sprintf("auth-dir: %q\nport: %d\nrequest-log: %t\n", authDir, port, port%2 == 0))
			}
			if errWrite := os.WriteFile(configPath, payload(8000), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			var reloads atomic.Int32
			w, errWatcher := NewWatcher(configPath, authDir, func(*config.Config) { reloads.Add(1) })
			if errWatcher != nil {
				t.Fatal(errWatcher)
			}
			w.SetConfig(&config.Config{AuthDir: authDir, Port: 8000})
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(func() {
				cancel()
				if errStop := w.Stop(); errStop != nil {
					t.Error(errStop)
				}
			})
			if errStart := w.Start(ctx); errStart != nil {
				t.Fatal(errStart)
			}
			for i, operation := range []string{"replace", "replace", "write", "recreate"} {
				port := 8001 + i
				body := payload(port)
				switch operation {
				case "replace":
					tmpPath := filepath.Join(dir, "config.yaml.tmp")
					if errWrite := os.WriteFile(tmpPath, body, 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
					if errRename := os.Rename(tmpPath, configPath); errRename != nil {
						t.Fatal(errRename)
					}
				case "recreate":
					if errRemove := os.Remove(configPath); errRemove != nil {
						t.Fatal(errRemove)
					}
					fallthrough
				case "write":
					if errWrite := os.WriteFile(configPath, body, 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
				}
				wantHash := fmt.Sprintf("%x", sha256.Sum256(body))
				deadline := time.Now().Add(3 * time.Second)
				for {
					w.clientsMutex.RLock()
					loaded := w.config.Port == port && w.config.RequestLog == (port%2 == 0) && w.lastConfigHash == wantHash
					w.clientsMutex.RUnlock()
					if loaded {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("config watch lost after %s #%d (port %d)", operation, i+1, port)
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			before := reloads.Load()
			if errWrite := os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("unrelated"), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			time.Sleep(2 * configReloadDebounce)
			if after := reloads.Load(); after != before {
				t.Fatalf("unrelated sibling file reloaded config: %d -> %d", before, after)
			}
		})
	}
}
