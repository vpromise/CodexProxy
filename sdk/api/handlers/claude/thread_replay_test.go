package claude

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

func TestClaudeThreadReplayPreservesErrorFields(t *testing.T) {
	body := []byte(`{
  "type":"error","request_id":"req_original",
  "error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id.","details":{"extra":"kept"}}
 }`)
	for _, mode := range []string{"JSON", "direct JSON", "wrapped JSON", "committed SSE", "direct SSE"} {
		t.Run(mode, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			msg := &interfaces.ErrorMessage{StatusCode: 404, Error: errors.New(string(body)), Body: body, DirectResponse: strings.HasPrefix(mode, "direct")}
			if mode == "wrapped JSON" {
				msg.Error = fmt.Errorf("wrapped: %w", threadReplayBodyError{body: body})
			}
			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			wantStatus := http.StatusNotFound
			streaming := strings.Contains(mode, "SSE")
			if streaming {
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.Write([]byte("event: message_start\ndata: {}\n\n"))
				c.Writer.Flush()
				errs := make(chan *interfaces.ErrorMessage, 1)
				errs <- msg
				close(errs)
				handler.forwardClaudeStream(c, c.Writer, func(err error) {
					if err != msg.Error {
						t.Errorf("unexpected cancellation: %v", err)
					}
				}, nil, errs)
				wantStatus = http.StatusOK
			} else {
				handler.WriteErrorResponse(c, msg)
			}
			got := recorder.Body.String()
			if streaming {
				const prefix = "event: error\ndata: "
				if strings.Count(got, prefix) != 1 {
					t.Fatalf("expected one terminal event: %s", got)
				}
				_, got, _ = strings.Cut(got, prefix)
				got = strings.TrimSpace(got)
				if strings.Contains(got, "\n") {
					t.Fatalf("multiline JSON corrupted SSE event: %s", got)
				}
			}
			if recorder.Code != wantStatus || gjson.Get(got, "error.details.error_code").String() != "thread_not_found" || gjson.Get(got, "error.details.extra").String() != "kept" || gjson.Get(got, "request_id").String() != "req_original" {
				t.Fatalf("unexpected replay status=%d body=%s", recorder.Code, got)
			}
		})
	}
}

type threadReplayBodyError struct{ body []byte }

func (threadReplayBodyError) Error() string          { return "upstream request failed" }
func (e threadReplayBodyError) ResponseBody() []byte { return e.body }

func TestClaudeThreadReplayDoesNotMarkOtherFailures(t *testing.T) {
	for _, tc := range []struct {
		status  int
		message string
	}{
		{404, "model not found"},
		{401, "No thread state was found for the requested previous_message_id."},
		{429, "No thread state was found for the requested previous_message_id."},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"type":"error","error":{"type":"not_found_error","message":%q}}`, tc.message))
			got := (&ClaudeCodeAPIHandler{}).claudeErrorBody(&interfaces.ErrorMessage{StatusCode: tc.status, Error: errors.New(string(body))})
			if gjson.GetBytes(got, "error.details.error_code").Exists() {
				t.Fatalf("unexpected replay marker: %s", got)
			}
		})
	}
}
