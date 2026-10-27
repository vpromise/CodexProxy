package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRequestUsageIdentitySurvivesGinReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, original := range []string{"first-client", ""} {
		t.Run("original="+original, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
			if original != "" {
				c.Set("userApiKey", original)
			}
			h := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
			ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
			cancel()
			// Simulate the pooled context being assigned to the next request.
			c.Keys = map[string]any{"userApiKey": "next-client"}
			c.Request = httptest.NewRequest("POST", "/v1/messages", nil)
			if got := helps.APIKeyFromContext(ctx); got != original {
				t.Fatalf("finished request identity = %q, want %q", got, original)
			}
		})
	}
}

func TestRequestUsageSnapshotDoesNotReadConcurrentGinReuse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", nil)
	c.Set("userApiKey", "first-client")
	h := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	ctx, cancel := h.GetContextWithCancel(nil, c, context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			c.Keys = map[string]any{"userApiKey": "next-client"}
		}
	}()
	for i := 0; i < 1000; i++ {
		if key := helps.APIKeyFromContext(ctx); key != "first-client" {
			t.Errorf("identity changed to %q", key)
			break
		}
	}
	<-done
}
