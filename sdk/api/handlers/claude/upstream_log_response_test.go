package claude

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
)

func TestClaudeFallbackKeepsUpstreamCapture(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx := context.WithValue(context.Background(), "gin", c)
	config := &config.Config{}
	config.RequestLog = true
	helps.RecordAPIResponseMetadata(ctx, config, http.StatusBadGateway, nil)
	helps.AppendAPIResponseChunk(ctx, config, []byte("anthropic response"))
	appendClaudeAPIResponse(c, []byte("synthetic claude response"))
	value, _ := c.Get("API_RESPONSE")
	body, _ := value.([]byte)
	if !bytes.Contains(body, []byte("anthropic response")) || bytes.Contains(body, []byte("synthetic claude response")) {
		t.Fatalf("upstream log = %q", body)
	}
}
