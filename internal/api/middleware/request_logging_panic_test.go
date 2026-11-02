package middleware

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
)

type panicRequestLogger struct {
	testRequestLogger
	writers []*testStreamingLogWriter
}

func (l *panicRequestLogger) LogStreamingRequest(string, string, map[string][]string, []byte, string) (logging.StreamingLogWriter, error) {
	w := &testStreamingLogWriter{}
	l.writers = append(l.writers, w)
	return w, nil
}

func TestRequestLoggingPanicFinalization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, committed := range []bool{false, true} {
		name := "uncommitted"
		if committed {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			logger := &panicRequestLogger{testRequestLogger: testRequestLogger{enabled: true}}
			router := gin.New()
			router.Use(logging.GinLogrusRecovery(), RequestLoggingMiddleware(logger))
			var completed *gin.Context
			router.POST("/v1/responses", func(c *gin.Context) {
				completed = c
				c.Set(logging.DeferredAPIRequestContextKey, []logging.DeferredAPIRequest{func() []byte { return []byte("retained") }})
				c.Header("Content-Type", "text/event-stream")
				c.Status(http.StatusOK)
				c.Status(http.StatusOK)
				if committed {
					_, _ = c.Writer.Write([]byte("data: chunk\n\n"))
				}
				panic("handler boom")
			})
			resp := httptest.NewRecorder()
			router.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"stream":true}`)))
			wantStatus := http.StatusInternalServerError
			if committed {
				wantStatus = http.StatusOK
			}
			if resp.Code != wantStatus {
				t.Errorf("status = %d, want %d", resp.Code, wantStatus)
			}
			if len(logger.writers) != 1 {
				t.Errorf("opened %d stream writers, want one", len(logger.writers))
			}
			for _, w := range logger.writers {
				if !w.closed {
					t.Error("stream writer not closed after panic")
				}
			}
			if v, _ := completed.Get(logging.DeferredAPIRequestContextKey); v != nil {
				t.Error("deferred request builders retained after panic")
			}
		})
	}
}

func TestRequestLoggingPanicErrorOnlyAndAbort(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value any
		write bool
		logs  int
	}{
		{"before status", "boom", false, 1},
		{"after committed success", "boom", true, 1},
		{"abort", http.ErrAbortHandler, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			logger := logging.NewFileRequestLogger(false, dir, "", 10)
			router := gin.New()
			router.Use(RequestLoggingMiddleware(logger))
			router.POST("/v1/responses", func(c *gin.Context) {
				if tc.write {
					_, _ = c.Writer.WriteString("partial")
				}
				panic(tc.value)
			})
			func() {
				defer func() {
					if got := recover(); got != tc.value {
						t.Errorf("panic = %v, want %v", got, tc.value)
					}
				}()
				router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test"}`)))
			}()
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != tc.logs {
				t.Fatalf("logs = %d, want %d", len(entries), tc.logs)
			}
			if tc.logs > 0 && !strings.HasPrefix(entries[0].Name(), "error-") {
				t.Fatal("expected error log")
			}
		})
	}
}
