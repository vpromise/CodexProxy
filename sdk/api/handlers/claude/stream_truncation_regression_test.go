package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type preFTKeepAliveClaudeExecutor struct {
	chunks chan coreexecutor.StreamChunk
}

func (*preFTKeepAliveClaudeExecutor) Identifier() string { return "pre-ft-keepalive-claude-executor" }

func (*preFTKeepAliveClaudeExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *preFTKeepAliveClaudeExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return &coreexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (*preFTKeepAliveClaudeExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*preFTKeepAliveClaudeExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*preFTKeepAliveClaudeExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestClaudeMessagesHandlerTruncatedStreamWithoutMessageStopWritesSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "truncated-stream-claude-model"
	auth := &coreauth.Auth{ID: "truncated-stream-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: ") {
		t.Fatalf("expected terminal SSE error event on truncated stream; body=%q", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("expected terminal error message to mention missing message_stop; body=%q", body)
	}
}

func TestClaudeStreamTerminalErrorAfterPartialLineStartsOnNewLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		data <- []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\"")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("stream cut mid-line"),
		}
		close(data)
		close(errs)
	}()

	handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

	body := recorder.Body.String()
	if !strings.Contains(body, "\nevent: error\ndata: ") {
		t.Fatalf("expected event: error to start on a fresh line after partial chunk; body=%q", body)
	}
}

func TestClaudeStreamCompleteWithMessageStopDoesNotEmitErrorOnClose(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "complete-stream-claude-model"
	auth := &coreauth.Auth{ID: "complete-stream-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("unexpected error event on completed stream: %q", recorder.Body.String())
	}
}

func TestClaudeStreamTerminalErrorAfterSingleNewlineInsertsBlankLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		// Ends with a single '\n' (complete line, but missing the blank-line SSE event separator).
		data <- []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("stream cut before blank line"),
		}
		close(data)
		close(errs)
	}()

	handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

	body := recorder.Body.String()
	if !strings.Contains(body, "\n\nevent: error\ndata: ") {
		t.Fatalf("expected blank-line SSE separator (\\n\\n) before event: error when prior chunk ended with a single newline; body=%q", body)
	}
}

func TestClaudeStreamBareCompletedMessageJSONDoesNotEmitErrorOnClose(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 1)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "bare-json-stream-claude-model"
	auth := &coreauth.Auth{ID: "bare-json-stream-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}],\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("unexpected error event after complete bare message JSON: %q", recorder.Body.String())
	}
}

func TestClaudeStreamMessageStopWithArbitraryJSONFormattingCompletesCleanly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "formatted-message-stop-claude-model"
	auth := &coreauth.Auth{ID: "formatted-message-stop-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event:message_start\ndata: {\"type\" : \"message_start\", \"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event:message_stop\ndata: {\"type\" : \"message_stop\"}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("unexpected error event when valid message_stop used custom spacing: %q", recorder.Body.String())
	}
}

func TestClaudeStreamTextDeltaMentioningMessageStopStillFailsWhenTruncated(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "false-positive-message-stop-claude-model"
	auth := &coreauth.Auth{ID: "false-positive-message-stop-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"emit\",\"input\":{\"type\":\"message_stop\"}}}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if !strings.Contains(recorder.Body.String(), "event: error\ndata: ") {
		t.Fatalf("expected truncated stream to emit terminal SSE error even if text delta mentioned message_stop; body=%q", recorder.Body.String())
	}
}

func TestClaudeStreamTruncatedErrorEventHeaderDoesNotSuppressTerminalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		// Upstream started an error event header but dropped before sending a valid data JSON frame.
		data <- []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\nevent: error\n")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("upstream stream closed before message_stop"),
		}
		close(data)
		close(errs)
	}()

	handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: {\"type\":\"error\"") {
		t.Fatalf("expected valid terminal SSE error frame after truncated event: error header; body=%q", body)
	}
}

func TestClaudeStreamErrorEventWithoutTrailingBlankLineIsTerminatedOnEOF(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{name: "zero trailing newlines", suffix: ""},
		{name: "single trailing newline", suffix: "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Header("Content-Type", "text/event-stream")

			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage)
			go func() {
				data <- []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"failed\"}}" + tc.suffix)
				errs <- &interfaces.ErrorMessage{
					StatusCode: http.StatusBadGateway,
					Error:      errors.New(`{"type":"error","error":{"type":"api_error","message":"failed"}}`),
				}
				close(data)
				close(errs)
			}()

			handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

			body := recorder.Body.String()
			want := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"failed\"}}\n\n"
			if body != want {
				t.Fatalf("SSE body = %q, want %q", body, want)
			}
		})
	}
}

func TestClaudeStreamMessageStopWithoutTrailingBlankLineIsTerminatedOnEOF(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{name: "zero trailing newlines", suffix: ""},
		{name: "single trailing newline", suffix: "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Header("Content-Type", "text/event-stream")

			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			data := make(chan []byte, 1)
			data <- []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}" + tc.suffix)
			close(data)

			handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, nil)

			body := recorder.Body.String()
			want := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			if body != want {
				t.Fatalf("SSE body = %q, want %q", body, want)
			}
		})
	}
}

func TestClaudeMessagesHandlerEmptyStreamClosesWithSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	done := make(chan struct{})
	go func() {
		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("forwardClaudeStream hung when data and errs channels closed without chunks")
	}

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: ") || !strings.Contains(body, "message_stop") {
		t.Fatalf("expected terminal SSE error event mentioning message_stop on empty stream; body=%q", body)
	}
}

func TestClaudeStreamMultiLineDataEventsAreRecognizedAcrossChunks(t *testing.T) {
	t.Run("multiline message_stop split across chunks", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Header("Content-Type", "text/event-stream")

		handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
		data := make(chan []byte, 2)
		data <- []byte("event: message_stop\ndata: {\"type\":\n")
		data <- []byte("data: \"message_stop\"}\n\n")
		close(data)

		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, nil)

		body := recorder.Body.String()
		if strings.Contains(body, "event: error") {
			t.Fatalf("unexpected error event after valid multi-line message_stop; body=%q", body)
		}
	})

	t.Run("multiline error event split across chunks", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Header("Content-Type", "text/event-stream")

		handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage)
		go func() {
			data <- []byte("event: error\ndata: {\"type\":\"error\",\n")
			data <- []byte("data: \"error\":{\"type\":\"api_error\",\"message\":\"boom\"}}\n\n")
			errs <- &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Error:      errors.New("boom"),
			}
			close(data)
			close(errs)
		}()

		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

		body := recorder.Body.String()
		if got := strings.Count(body, "event: error"); got != 1 {
			t.Fatalf("event: error count = %d, want 1; body=%q", got, body)
		}
	})

	t.Run("multiline tool_use with standalone nested message_stop line still fails when truncated", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Header("Content-Type", "text/event-stream")

		handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
		data := make(chan []byte, 1)
		data <- []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"emit\",\"input\":\ndata: {\"type\":\"message_stop\"}\ndata: }}\n\n")
		close(data)

		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, nil)

		body := recorder.Body.String()
		if !strings.Contains(body, "event: error\ndata: ") {
			t.Fatalf("expected terminal error event when stream truncated after multiline tool_use containing nested message_stop line; body=%q", body)
		}
	})
}

func TestClaudeMessagesHandlerEmptyUpstreamPreservesInitialErrorStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: t.Name(), Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(t.Context(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: t.Name()}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	handler := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
	router := gin.New()
	router.POST("/v1/messages", handler.ClaudeMessages)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"`+t.Name()+`","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "empty_stream") {
		t.Fatalf("empty upstream accepted: %d %s", response.Code, response.Body.String())
	}
}
