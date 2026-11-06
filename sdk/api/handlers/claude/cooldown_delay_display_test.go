package claude

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/tidwall/gjson"
)

type displayedCooldownDelay struct {
	error
	delay time.Duration
}

func (e displayedCooldownDelay) Unwrap() error                        { return e.error }
func (e displayedCooldownDelay) DownstreamRetryAfter() *time.Duration { return &e.delay }

func TestClaudeQuotaCooldownDisplaysClientRetryDelay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		delay           time.Duration
		header, display string
	}{
		{8250 * time.Second, "60", "1m0s"},
		{1100 * time.Millisecond, "2", "2s"},
		{60*time.Second + time.Millisecond, "60", "1m0s"},
		{time.Duration(1<<63 - 1), "60", "1m0s"},
	} {
		t.Run(fmt.Sprint(tc.delay), func(t *testing.T) {
			err := displayedCooldownDelay{error: claudeTestCooldownError(t, true), delay: tc.delay}
			msg := &interfaces.ErrorMessage{StatusCode: 429, Error: err, Addon: http.Header{"Retry-After": {"9999"}}}
			h := &ClaudeCodeAPIHandler{BaseAPIHandler: &handlers.BaseAPIHandler{}}
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			h.WriteErrorResponse(c, msg)
			if rec.Code != 429 || rec.Header().Get("Retry-After") != tc.header {
				t.Fatalf("headers/status: %d %v", rec.Code, rec.Header())
			}
			message := gjson.GetBytes(rec.Body.Bytes(), "error.message").String()
			want := "Rate limit reached. Please try again later. (retry after " + tc.display + ")"
			if message != want {
				t.Fatalf("message=%q, want %q", message, want)
			}
			if strings.Contains(message, "private-") || strings.Contains(message, "reset after") {
				t.Fatalf("leaked context or falsely promised quota reset: %q", message)
			}
			streamRec := httptest.NewRecorder()
			sc, _ := gin.CreateTestContext(streamRec)
			sc.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			sc.Writer.WriteHeaderNow()
			data := make(chan []byte)
			close(data)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- msg
			close(errs)
			h.forwardClaudeStream(sc, sc.Writer, func(error) {}, data, errs)
			if !strings.Contains(streamRec.Body.String(), want) || streamRec.Code != 200 {
				t.Fatalf("SSE message=%s", streamRec.Body.String())
			}
		})
	}
}

func TestClaudeCapacityCooldownKeepsExistingDisplay(t *testing.T) {
	msg := &interfaces.ErrorMessage{StatusCode: 429, Error: displayedCooldownDelay{error: claudeTestCooldownError(t, false), delay: time.Second}}
	got := (&ClaudeCodeAPIHandler{}).toClaudeError(msg)
	if got.Error.Type != "overloaded_error" || got.Error.Message != "Overloaded." {
		t.Fatalf("capacity display changed: %+v", got)
	}
}
