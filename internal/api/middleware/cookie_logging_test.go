package middleware

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestRequestLoggingMasksCookiesAcrossRequestAndResponseSections(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		stream  bool
	}{
		{name: "full nonstream", enabled: true},
		{name: "full stream", enabled: true, stream: true},
		{name: "error-only nonstream"},
		{name: "error-only stream", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			logsDir := t.TempDir()
			logger := logging.NewFileRequestLogger(tc.enabled, logsDir, "", 10)
			cfg := &config.Config{}
			cfg.RequestLog = tc.enabled
			secrets := []string{
				"synthetic-client-cookie-secret-value",
				"synthetic-upstream-request-cookie-secret-value",
				"synthetic-upstream-response-cookie-secret-value",
				"synthetic-downstream-response-cookie-secret-value",
			}
			router := gin.New()
			router.Use(RequestLoggingMiddleware(logger))
			router.POST("/v1/responses", func(c *gin.Context) {
				ctx := context.WithValue(c.Request.Context(), "gin", c)
				upstreamHeaders := http.Header{"Cookie": {"session=" + secrets[1]}, "X-Audit-Stage": {"upstream-request-marker"}}
				helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
					URL: "https://example.invalid/responses", Method: http.MethodPost,
					Headers: upstreamHeaders, Body: []byte(`{"model":"fixture"}`),
				})
				helps.RecordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{
					"Set-Cookie": {"session=" + secrets[2] + "; HttpOnly"}, "X-Audit-Stage": {"upstream-response-marker"},
				})
				helps.AppendAPIResponseChunk(ctx, cfg, []byte(`{"output":"fixture"}`))
				if got := upstreamHeaders.Get("Cookie"); got != "session="+secrets[1] {
					t.Errorf("logging changed upstream Cookie: %q", got)
				}
				if got := c.GetHeader("Cookie"); got != "session="+secrets[0] {
					t.Errorf("logging changed client Cookie: %q", got)
				}
				c.Header("Set-Cookie", "session="+secrets[3]+"; HttpOnly")
				c.Header("X-Audit-Stage", "downstream-response-marker")
				if !tc.enabled {
					c.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{{StatusCode: http.StatusBadGateway, Error: io.ErrUnexpectedEOF}})
				}
				if tc.stream {
					c.Header("Content-Type", "text/event-stream")
					c.Writer.WriteHeader(http.StatusOK)
					_, _ = c.Writer.Write([]byte("data: {\"output\":\"fixture\"}\n\n"))
					c.Writer.Flush()
					return
				}
				status := http.StatusOK
				if !tc.enabled {
					status = http.StatusBadGateway
				}
				c.JSON(status, gin.H{"output": "fixture"})
			})
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"fixture"}`))
			request.Header.Set("Cookie", "session="+secrets[0])
			request.Header.Set("X-Audit-Stage", "client-request-marker")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if got := response.Header().Get("Set-Cookie"); got != "session="+secrets[3]+"; HttpOnly" {
				t.Fatalf("logging changed downstream Set-Cookie: %q", got)
			}
			paths, errGlob := filepath.Glob(filepath.Join(logsDir, "*.log"))
			if errGlob != nil || len(paths) != 1 {
				t.Fatalf("log paths=%v error=%v, want one file", paths, errGlob)
			}
			data, errRead := os.ReadFile(paths[0])
			if errRead != nil {
				t.Fatal(errRead)
			}
			text := string(data)
			for _, secret := range secrets {
				if strings.Contains(text, secret) {
					t.Errorf("log leaked synthetic cookie credential %q", secret)
				}
			}
			markers := []string{"client-request-marker", "upstream-request-marker", "downstream-response-marker", "Cookie:", "Set-Cookie:"}
			if tc.enabled {
				markers = append(markers, "upstream-response-marker")
			}
			for _, marker := range markers {
				if !strings.Contains(text, marker) {
					t.Errorf("log omitted expected section/header marker %q", marker)
				}
			}
		})
	}
}
