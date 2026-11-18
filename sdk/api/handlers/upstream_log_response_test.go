package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestHandlerFallbackKeepsUpstreamCapture(t *testing.T) {
	const fallback = "synthetic downstream response"
	config := &config.Config{}
	config.RequestLog = true
	for _, mode := range []string{"no capture", "memory", "file"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx := context.WithValue(context.Background(), "gin", c)
			var source *logging.FileBodySource
			if mode == "file" {
				var err error
				source, err = logging.NewFileBodySourceInDir(t.TempDir(), "upstream")
				if err != nil {
					t.Fatal(err)
				}
				c.Set(logging.APIResponseSourceContextKey, source)
			}
			if mode != "no capture" {
				helps.RecordAPIResponseMetadata(ctx, config, http.StatusBadGateway, nil)
				helps.AppendAPIResponseChunk(ctx, config, []byte("upstream response"))
			}
			appendAPIResponse(c, []byte(fallback))

			value, _ := c.Get("API_RESPONSE")
			body, _ := value.([]byte)
			if mode == "no capture" {
				if string(body) != fallback {
					t.Fatalf("fallback log = %q", body)
				}
				return
			}
			if bytes.Contains(body, []byte(fallback)) {
				t.Fatalf("upstream log contains handler response: %q", body)
			}
			if source != nil {
				var err error
				body, err = source.Bytes()
				if err != nil {
					t.Fatal(err)
				}
			}
			if !bytes.Contains(body, []byte("upstream response")) {
				t.Fatalf("upstream response missing from log: %q", body)
			}
		})
	}
}
