package middleware

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFinalizeExcludesUnrouted404FromForceLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/files", nil)

	logger := &recordingRequestLogger{enabled: false}
	wrapper := &ResponseWriterWrapper{
		ResponseWriter: c.Writer,
		logger:         logger,
		logOnErrorOnly: true,
		statusCode:     http.StatusNotFound,
		requestInfo: &RequestInfo{
			URL:       "/v1/files",
			Method:    "POST",
			RequestID: "req-404",
			Timestamp: time.Now(),
		},
	}

	if err := wrapper.Finalize(c); err != nil {
		t.Fatalf("Finalize error: %v", err)
	}
	if len(logger.loggedCalls) != 0 {
		t.Fatalf("expected 0 logged calls for unrouted 404, got %d: %v", len(logger.loggedCalls), logger.loggedCalls)
	}
}

func TestFinalizeLogsUnrouted404WhenUpstreamEvidenceOrPanicExists(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("deferred api request recorded", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/files", nil)
		c.Set(logging.DeferredAPIRequestContextKey, []logging.DeferredAPIRequest{
			func() []byte { return []byte("=== API REQUEST 1 ===\n") },
		})

		logger := &recordingRequestLogger{enabled: false}
		wrapper := &ResponseWriterWrapper{
			ResponseWriter: c.Writer,
			logger:         logger,
			logOnErrorOnly: true,
			statusCode:     http.StatusNotFound,
			requestInfo:    &RequestInfo{URL: "/v1/files", Method: http.MethodPost, Timestamp: time.Now()},
		}
		if err := wrapper.Finalize(c); err != nil {
			t.Fatalf("Finalize error: %v", err)
		}
		if len(logger.loggedCalls) != 1 || logger.loggedCalls[0] != http.StatusNotFound {
			t.Fatalf("expected 1 logged call when deferred API request exists, got: %v", logger.loggedCalls)
		}
	})

	t.Run("file body source with payload recorded", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/files", nil)

		source, errSource := logging.NewFileBodySourceInDir(t.TempDir(), "api-request")
		if errSource != nil {
			t.Fatalf("NewFileBodySourceInDir: %v", errSource)
		}
		defer func() { _ = source.Cleanup() }()
		part, errPart := source.CreatePart("req")
		if errPart != nil {
			t.Fatalf("CreatePart: %v", errPart)
		}
		if _, errWrite := part.WriteString("upstream request payload"); errWrite != nil {
			t.Fatalf("WriteString: %v", errWrite)
		}
		if errClose := part.Close(); errClose != nil {
			t.Fatalf("Close: %v", errClose)
		}
		c.Set(logging.APIRequestSourceContextKey, source)

		logger := &recordingRequestLogger{enabled: false}
		wrapper := &ResponseWriterWrapper{
			ResponseWriter: c.Writer,
			logger:         logger,
			logOnErrorOnly: true,
			statusCode:     http.StatusNotFound,
			requestInfo:    &RequestInfo{URL: "/v1/files", Method: http.MethodPost, Timestamp: time.Now()},
		}
		if err := wrapper.Finalize(c); err != nil {
			t.Fatalf("Finalize error: %v", err)
		}
		if len(logger.loggedCalls) != 1 || logger.loggedCalls[0] != http.StatusNotFound {
			t.Fatalf("expected 1 logged call when file body source payload exists, got: %v", logger.loggedCalls)
		}
	})

	t.Run("panicked handler on 404 still logs", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/files", nil)

		logger := &recordingRequestLogger{enabled: false}
		wrapper := &ResponseWriterWrapper{
			ResponseWriter: c.Writer,
			logger:         logger,
			logOnErrorOnly: true,
			panicked:       true,
			statusCode:     http.StatusNotFound,
			requestInfo:    &RequestInfo{URL: "/v1/files", Method: http.MethodPost, Timestamp: time.Now()},
		}
		if err := wrapper.Finalize(c); err != nil {
			t.Fatalf("Finalize error: %v", err)
		}
		if len(logger.loggedCalls) != 1 || logger.loggedCalls[0] != http.StatusNotFound {
			t.Fatalf("expected 1 logged call when panicked=true, got: %v", logger.loggedCalls)
		}
	})
}
