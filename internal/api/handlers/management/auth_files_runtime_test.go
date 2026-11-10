package management

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestAuthUploadConcurrentRuntimeReload(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	manager := coreauth.NewManager(nil, nil, nil)
	handler := &Handler{cfg: cfg, authManager: manager}
	start := make(chan struct{})
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		for range 200 {
			handler.SetConfig(cfg.CloneForRuntime())
			handler.SetAuthManager(manager)
		}
	}()
	go func() {
		defer group.Done()
		<-start
		for range 200 {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest("POST", "/?name=invalid.txt", strings.NewReader("invalid"))
			handler.UploadAuthFile(c)
			if err := handler.writeAuthFile(t.Context(), "invalid.json", []byte("invalid JSON")); err == nil {
				t.Error("invalid JSON accepted")
			}
		}
	}()
	close(start)
	group.Wait()
}

type blockedAuthUploadBody struct {
	io.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockedAuthUploadBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release })
	return b.Reader.Read(p)
}

func TestAuthUploadKeepsOneRuntimeSnapshot(t *testing.T) {
	for _, multipartUpload := range []bool{false, true} {
		t.Run(fmt.Sprint(multipartUpload), func(t *testing.T) {
			firstDir, secondDir := t.TempDir(), t.TempDir()
			cfg := &config.Config{AuthDir: firstDir, AccessIsolation: config.AccessIsolation{Enabled: true}}
			firstManager, secondManager := coreauth.NewManager(nil, nil, nil), coreauth.NewManager(nil, nil, nil)
			hooks := make(chan string, 3)
			h := &Handler{cfg: cfg, authManager: firstManager, postAuthPersistHook: func(context.Context, *coreauth.Auth) error { hooks <- "first"; return nil }}
			var body bytes.Buffer
			contentType := "application/json"
			names := []string{"synthetic.json"}
			if multipartUpload {
				names = append(names, "second.json")
				writer := multipart.NewWriter(&body)
				for _, name := range names {
					part, err := writer.CreateFormFile("files", name)
					if err != nil {
						t.Fatal(err)
					}
					if _, errWrite := io.WriteString(part, `{"type":"codex","account_id":"synthetic-principal"}`); errWrite != nil {
						t.Fatal(errWrite)
					}
				}
				if errClose := writer.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				contentType = writer.FormDataContentType()
			} else {
				body.WriteString(`{"type":"codex","account_id":"synthetic-principal"}`)
			}
			blocked := &blockedAuthUploadBody{Reader: &body, started: make(chan struct{}), release: make(chan struct{})}
			var unblockOnce sync.Once
			unblock := func() { unblockOnce.Do(func() { close(blocked.release) }) }
			t.Cleanup(unblock)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/?name=synthetic.json", blocked)
			c.Request.Header.Set("Content-Type", contentType)
			done := make(chan struct{})
			go func() { h.UploadAuthFile(c); close(done) }()
			select {
			case <-blocked.started:
			case <-time.After(3 * time.Second):
				t.Fatal("upload did not read body")
			}
			reloaded := make(chan struct{})
			go func() {
				h.mu.Lock()
				h.cfg.AuthDir = secondDir
				h.cfg.AccessIsolation.Enabled = false
				h.mu.Unlock()
				h.SetConfig(&config.Config{AuthDir: secondDir})
				h.SetAuthManager(secondManager)
				h.SetPostAuthPersistHook(func(context.Context, *coreauth.Auth) error { hooks <- "second"; return nil })
				close(reloaded)
			}()
			select {
			case <-reloaded:
			case <-time.After(3 * time.Second):
				unblock()
				t.Fatal("upload body blocks runtime reload")
			}
			unblock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("upload did not finish")
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("upload: %d %s", recorder.Code, recorder.Body.String())
			}
			if len(secondManager.List()) != 0 {
				t.Fatal("upload used replacement manager")
			}
			for _, name := range names {
				if _, err := os.Stat(filepath.Join(firstDir, name)); err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(filepath.Join(secondDir, name)); !os.IsNotExist(err) {
					t.Fatalf("replacement directory contains upload: %v", err)
				}
				account, exists := firstManager.GetByID(name)
				if !exists || coreauth.AccountIdentity(account) == coreauth.AccountSubject(account) {
					t.Fatal("upload lost its captured ID directory or enrollment policy")
				}
				select {
				case hook := <-hooks:
					if hook != "first" {
						t.Fatal("upload used replacement persist hook")
					}
				default:
					t.Fatal("persist hook missing")
				}
			}
		})
	}
}
