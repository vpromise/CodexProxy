package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestSyncOctober23PromptOverflowAfterCommittedStream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, ua := range []string{"codex_cli_rs/0.1.0", "generic-sdk"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.Request.Header.Set("User-Agent", ua)
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString(": keep-alive\n\n")
		c.Writer.Flush()
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage, 1)
		errs <- &interfaces.ErrorMessage{StatusCode: 400, Error: errors.New(`{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 > 200000"}}`)}
		close(errs)
		h.forwardResponsesStream(c, c.Writer, func(error) {}, data, errs, &responsesSSEFramer{})
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `"code":"context_length_exceeded"`) || strings.Contains(body, "response.completed") {
			t.Fatalf("%s: %d %s", ua, rec.Code, body)
		}
		if ua != "generic-sdk" && !strings.Contains(body, "event: response.failed") {
			t.Fatal(body)
		}
		if ua == "generic-sdk" && !strings.Contains(body, "event: error") {
			t.Fatal(body)
		}
	}
}
