package api

import (
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestServerLoggingCapturesExtraMiddlewareRecovery(t *testing.T) {
	dir := t.TempDir()
	server := newTestServerWithOptions(t,
		WithRequestLoggerFactory(func(*config.Config, string) logging.RequestLogger {
			return logging.NewFileRequestLogger(false, dir, "", 10)
		}),
		WithMiddleware(func(c *gin.Context) {
			defer func() {
				if recover() != nil {
					c.AbortWithStatusJSON(503, gin.H{"error": "custom recovery"})
				}
			}()
			c.Next()
		}),
	)
	server.engine.POST("/v1/panic-probe", func(c *gin.Context) { panic("boom") })
	resp := httptest.NewRecorder()
	server.engine.ServeHTTP(resp, httptest.NewRequest(http.MethodPost, "/v1/panic-probe", nil))
	if resp.Code != 503 {
		t.Fatalf("status = %d", resp.Code)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("logs = %d", len(files))
	}
	content, err := os.ReadFile(dir + "/" + files[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "custom recovery") {
		t.Fatal("custom recovery response absent from request log")
	}
}
