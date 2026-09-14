package claude

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func claudeTestCooldownError(t *testing.T, quota bool) error {
	t.Helper()
	next := time.Now().Add(time.Hour)
	state := &coreauth.ModelState{Unavailable: true, NextRetryAfter: next, LastError: &coreauth.Error{HTTPStatus: 529}}
	if quota {
		state.Quota = coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next}
	}
	_, errPick := (&coreauth.FillFirstSelector{}).Pick(context.Background(), "claude", "private-model", cliproxyexecutor.Options{}, []*coreauth.Auth{
		{ID: "private-credential", ModelStates: map[string]*coreauth.ModelState{"private-model": state}},
	})
	if !coreauth.IsModelCooldownError(errPick) {
		t.Fatalf("expected cooldown error, got %v", errPick)
	}
	return fmt.Errorf("private-context: %w", errPick)
}

func TestClaudeCooldownErrorDistinguishesQuotaAndCapacity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, quota := range []bool{true, false} {
		t.Run(fmt.Sprint(quota), func(t *testing.T) {
			wantStatus, wantType := 529, "overloaded_error"
			if quota {
				wantStatus, wantType = http.StatusTooManyRequests, "rate_limit_error"
			}
			msg := &interfaces.ErrorMessage{StatusCode: http.StatusTooManyRequests, Error: claudeTestCooldownError(t, quota)}
			handler := &ClaudeCodeAPIHandler{BaseAPIHandler: &handlers.BaseAPIHandler{}}
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			handler.WriteErrorResponse(ctx, msg)
			if recorder.Code != wantStatus || gjson.GetBytes(recorder.Body.Bytes(), "error.type").String() != wantType {
				t.Fatalf("unexpected cooldown response: status=%d body=%s", recorder.Code, recorder.Body)
			}
			if recorder.Header().Get("Retry-After") != "60" || recorder.Header().Get("Request-Id") == "" {
				t.Fatalf("missing retry/request metadata: %v", recorder.Header())
			}
			if strings.Contains(recorder.Body.String(), "private-") {
				t.Fatal("cooldown response leaked internal context")
			}

			streamRecorder := httptest.NewRecorder()
			streamCtx, _ := gin.CreateTestContext(streamRecorder)
			streamCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			streamCtx.Writer.WriteHeaderNow()
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- msg
			close(errs)
			data := make(chan []byte)
			close(data)
			handler.forwardClaudeStream(streamCtx, streamCtx.Writer, func(error) {}, data, errs)
			body := streamRecorder.Body.String()
			if streamRecorder.Code != http.StatusOK || !strings.Contains(body, "event: error\n") || !strings.Contains(body, `"type":"`+wantType+`"`) || strings.Contains(body, "private-") {
				t.Fatalf("unexpected streaming cooldown: status=%d body=%s", streamRecorder.Code, body)
			}
		})
	}
}

func TestClaudeCooldownDirectResponsePreservesUpstreamContract(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const body = `{"type":"error","error":{"type":"overloaded_error","message":"Upstream overloaded"}}`
	msg := &interfaces.ErrorMessage{StatusCode: 529, DirectResponse: true, Error: claudeTestCooldownError(t, true), Body: []byte(body), Headers: http.Header{"Request-Id": {"req_upstream"}}}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	(&ClaudeCodeAPIHandler{}).WriteErrorResponse(ctx, msg)
	if recorder.Code != 529 || recorder.Body.String() != body || recorder.Header().Get("Request-Id") != "req_upstream" {
		t.Fatalf("direct upstream response changed: %d %s", recorder.Code, recorder.Body)
	}
}
