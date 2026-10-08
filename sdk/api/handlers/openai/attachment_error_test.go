package openai

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestChatEndpointRejectsUnrepresentableResponsesTurn(t *testing.T) {
	for _, stream := range []bool{false, true} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"test","stream":%t,"input":[{"role":"user","content":[{"type":"input_file","file_url":"https://fixture.invalid/file.pdf"}]}]}`, stream)))
		h := NewOpenAIAPIHandler(&handlers.BaseAPIHandler{})
		h.ChatCompletions(c)
		if rec.Code != 400 || !strings.Contains(rec.Body.String(), "input_file") {
			t.Fatalf("response=%d %s", rec.Code, rec.Body.String())
		}
	}
}
