package logging

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestGinLogrusLoggerEscapesEncodedNewlineInPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	hook := logtest.NewLocal(logger)
	logger.SetLevel(log.InfoLevel)
	t.Cleanup(func() { logger.ReplaceHooks(previousHooks); logger.SetLevel(previousLevel) })

	engine := gin.New()
	engine.Use(GinLogrusLogger())

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/x%0A200%20forged", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNotFound)
	}
	entries := hook.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	message := entries[0].Message
	if strings.ContainsAny(message, "\r\n") {
		t.Errorf("access log message contains a line break: %q", message)
	}
	if !strings.HasSuffix(message, `GET     "/x\n200 forged"`) {
		t.Errorf("access log message = %q, want suffix %q", message, `GET     "/x\n200 forged"`)
	}
}

func TestGinLogrusLoggerKeepsMaskedQueryReadable(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	hook := logtest.NewLocal(logger)
	logger.SetLevel(log.InfoLevel)
	t.Cleanup(func() { logger.ReplaceHooks(previousHooks); logger.SetLevel(previousLevel) })

	engine := gin.New()
	engine.Use(GinLogrusLogger())
	engine.GET("/v1/models", func(c *gin.Context) { c.Status(http.StatusOK) })

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models?key=sk-0123456789cdef&limit=5", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	entries := hook.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	message := entries[0].Message
	wantSuffix := `GET     "/v1/models?key=sk-0...cdef&limit=5"`
	if !strings.HasSuffix(message, wantSuffix) {
		t.Errorf("access log message = %q, want suffix %q", message, wantSuffix)
	}
}
