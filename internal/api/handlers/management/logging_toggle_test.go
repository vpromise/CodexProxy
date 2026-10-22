package management

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
)

func TestFileLoggingEnableMakesNewApplicationLogsReadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("WRITABLE_PATH", dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("logging-to-file: false\nrequest-log: false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path, logDir: filepath.Join(dir, "logs")}
	if err := logging.ConfigureLogOutput(cfg); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := logging.ConfigureLogOutput(&config.Config{}); err != nil {
			t.Error(err)
		}
	})
	call := func(fn gin.HandlerFunc, method, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(method, "/", bytes.NewBufferString(body))
		fn(c)
		return rec
	}
	if got := call(h.GetLogs, http.MethodGet, "").Code; got != 400 {
		t.Fatalf("disabled log status = %d", got)
	}
	applied := make(chan error, 1)
	h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { applied <- logging.ConfigureLogOutput(next) })
	if got := call(h.PutLoggingToFile, http.MethodPut, `{"value":true}`).Code; got != 200 {
		t.Fatalf("enable status = %d", got)
	}
	select {
	case err := <-applied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("logging reload missing")
	}
	log.Info("synthetic application log after enabling")
	response := call(h.GetLogs, http.MethodGet, "")
	if response.Code != 200 || !strings.Contains(response.Body.String(), "synthetic application log after enabling") {
		t.Fatalf("enabled log query status = %d; marker missing", response.Code)
	}
	persisted, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.LoggingToFile || persisted.RequestLog {
		t.Fatal("file logging toggle changed full request logging")
	}
}
