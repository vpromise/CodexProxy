package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type unroutedLogSpy struct {
	logging.RequestLogger
	lastStreamingRequestID string
	lastStreamWriter       logging.StreamingLogWriter
}

func (s *unroutedLogSpy) LogStreamingRequest(url, method string, headers map[string][]string, body []byte, requestID string) (logging.StreamingLogWriter, error) {
	s.lastStreamingRequestID = requestID
	w, err := s.RequestLogger.LogStreamingRequest(url, method, headers, body, requestID)
	s.lastStreamWriter = w
	return w, err
}
func TestRequestLoggingMiddleware_Unrouted404DoesNotEvictErrorDumpRing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("unrouted 404 does not create error log or evict existing error dumps when request-log is false", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/messages", func(c *gin.Context) {
			c.JSON(http.StatusBadGateway, gin.H{"error": "upstream failure"})
		})
		router.NoRoute(func(c *gin.Context) {
			c.AbortWithStatus(http.StatusNotFound)
		})

		realFailReq := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`))
		realFailReq.Header.Set("Content-Type", "application/json")
		realFailResp := httptest.NewRecorder()
		router.ServeHTTP(realFailResp, realFailReq)
		if realFailResp.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want %d", realFailResp.Code, http.StatusBadGateway)
		}

		for i := 0; i < 10; i++ {
			probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"purpose":"user_data"}`))
			probeReq.Header.Set("Content-Type", "application/json")
			probeResp := httptest.NewRecorder()
			router.ServeHTTP(probeResp, probeReq)
			if probeResp.Code != http.StatusNotFound {
				t.Fatalf("probe %d status = %d, want %d", i, probeResp.Code, http.StatusNotFound)
			}
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if len(names) != 1 || !strings.HasPrefix(names[0], "error-v1-messages-") {
			t.Fatalf("expected only the original error-v1-messages-*.log dump to survive, got: %v", names)
		}
	})

	t.Run("unrouted 404 does not create error log when request-log is true", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(true, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/messages", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		router.NoRoute(func(c *gin.Context) {
			c.AbortWithStatus(http.StatusNotFound)
		})

		probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"purpose":"user_data"}`))
		probeReq.Header.Set("Content-Type", "application/json")
		probeResp := httptest.NewRecorder()
		router.ServeHTTP(probeResp, probeReq)
		if probeResp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", probeResp.Code, http.StatusNotFound)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil && !os.IsNotExist(errRead) {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("expected 0 files for unrouted 404 when request-log=true, got: %v", names)
		}
	})

	t.Run("routed 404 still creates error log when request-log is false", func(t *testing.T) {
		logsDir := t.TempDir()
		logger := logging.NewFileRequestLogger(false, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(logger))
		router.POST("/v1/messages", func(c *gin.Context) {
			c.JSON(http.StatusNotFound, gin.H{"error": "model not found"})
		})

		req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"missing-model"}`))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		router.ServeHTTP(resp, req)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", resp.Code, http.StatusNotFound)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "error-v1-messages-") {
			t.Fatalf("expected 1 error-v1-messages-*.log for routed 404, got: %+v", entries)
		}
	})

	t.Run("unrouted streaming 404 does not initialize stream writer or leak temp files when request-log is true", func(t *testing.T) {
		logsDir := t.TempDir()
		fileLogger := logging.NewFileRequestLogger(true, logsDir, "", 5)
		spy := &unroutedLogSpy{RequestLogger: fileLogger}

		router := gin.New()
		router.Use(RequestLoggingMiddleware(spy))
		router.NoRoute(func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.Writer.WriteHeader(http.StatusNotFound)
			_, _ = c.Writer.Write([]byte("event: error\ndata: not found\n\n"))
		})

		probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"stream":true}`))
		probeReq.Header.Set("Content-Type", "application/json")
		probeResp := httptest.NewRecorder()
		router.ServeHTTP(probeResp, probeReq)
		if probeResp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", probeResp.Code, http.StatusNotFound)
		}
		if spy.lastStreamingRequestID != "" || spy.lastStreamWriter != nil {
			t.Fatalf("expected LogStreamingRequest not to be called for unrouted 404, got requestID=%q writer=%v", spy.lastStreamingRequestID, spy.lastStreamWriter)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil && !os.IsNotExist(errRead) {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("expected 0 files/temp dirs for unrouted streaming 404, got: %v", names)
		}
	})

	t.Run("unrouted streaming 200 to uncommitted 404 transition discards stream writer without creating error log", func(t *testing.T) {
		logsDir := t.TempDir()
		fileLogger := logging.NewFileRequestLogger(true, logsDir, "", 5)

		router := gin.New()
		router.Use(RequestLoggingMiddleware(fileLogger))
		router.NoRoute(func(c *gin.Context) {
			c.Header("Content-Type", "text/event-stream")
			c.Writer.WriteHeader(http.StatusOK)
			c.Writer.WriteHeader(http.StatusNotFound)
			_, _ = c.Writer.Write([]byte("data: not found\n\n"))
		})

		probeReq := httptest.NewRequest(http.MethodPost, "/v1/files", strings.NewReader(`{"stream":true}`))
		probeReq.Header.Set("Content-Type", "application/json")
		probeResp := httptest.NewRecorder()
		router.ServeHTTP(probeResp, probeReq)
		if probeResp.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d", probeResp.Code, http.StatusNotFound)
		}

		entries, errRead := os.ReadDir(logsDir)
		if errRead != nil && !os.IsNotExist(errRead) {
			t.Fatalf("read logs dir: %v", errRead)
		}
		if len(entries) != 0 {
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			t.Fatalf("expected 0 files/temp dirs after discarded streaming 404, got: %v", names)
		}
	})
}
