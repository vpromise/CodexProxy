// Package claude provides HTTP handlers for Claude API code-related functionality.
// This package implements Claude-compatible streaming chat completions with sophisticated
// client rotation and quota management systems to ensure high availability and optimal
// resource utilization across multiple backend clients. It handles request translation
// between Claude API format and the configured backend, providing seamless
// API compatibility while maintaining robust error handling and connection management.
package claude

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v7/internal/client/claude/models"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ClaudeCodeAPIHandler contains the handlers for Claude API endpoints.
// It holds a pool of clients to interact with the backend service.
type ClaudeCodeAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewClaudeCodeAPIHandler creates a new Claude API handlers instance.
// It takes an BaseAPIHandler instance as input and returns a ClaudeCodeAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handler instance.
//
// Returns:
//   - *ClaudeCodeAPIHandler: A new Claude code API handler instance.
func NewClaudeCodeAPIHandler(apiHandlers *handlers.BaseAPIHandler) *ClaudeCodeAPIHandler {
	return &ClaudeCodeAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *ClaudeCodeAPIHandler) HandlerType() string {
	return Claude
}

// Models returns a list of models supported by this handler.
func (h *ClaudeCodeAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("claude")
}

// ClaudeMessages handles Claude-compatible streaming chat completions.
// This function implements a sophisticated client rotation and quota management system
// to ensure high availability and optimal resource utilization across multiple backend clients.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeMessages(c *gin.Context) {
	EnsureRequestID(c)
	rawJSON, ok := readClaudeRequestBody(c)
	if !ok {
		return
	}

	// Decode claude-fable-5-dd-<reversed> model IDs back to the real model name for routing.
	rawJSON = rewriteClaudeDDModelInBody(rawJSON)

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if !streamResult.Exists() || streamResult.Type == gjson.False {
		h.handleNonStreamingResponse(c, rawJSON)
	} else {
		h.handleStreamingResponse(c, rawJSON)
	}
}

// ClaudeMessages handles Claude-compatible streaming chat completions.
// This function implements a sophisticated client rotation and quota management system
// to ensure high availability and optimal resource utilization across multiple backend clients.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeCountTokens(c *gin.Context) {
	EnsureRequestID(c)
	rawJSON, ok := readClaudeRequestBody(c)
	if !ok {
		return
	}

	// Decode claude-fable-5-dd-<reversed> model IDs back to the real model name for routing.
	rawJSON = rewriteClaudeDDModelInBody(rawJSON)

	c.Header("Content-Type", "application/json")

	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	modelName := gjson.GetBytes(rawJSON, "model").String()

	resp, upstreamHeaders, errMsg := h.ExecuteCountWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	writeClaudeUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

func readClaudeRequestBody(c *gin.Context) ([]byte, bool) {
	rawJSON, err := handlers.ReadStrictJSONRequestBody(c, RequestBodyMaxBytes)
	if err == nil {
		return rawJSON, true
	}

	status := http.StatusBadRequest
	message := fmt.Sprintf("Invalid request: %v", err)
	if errors.Is(err, handlers.ErrRequestBodyTooLarge) {
		status = http.StatusRequestEntityTooLarge
		message = fmt.Sprintf("Request body exceeds the %d byte limit", RequestBodyMaxBytes)
	}
	WriteProtocolError(c, status, message)
	return nil, false
}

// rewriteClaudeDDModelInBody decodes model IDs of the form claude-fable-5-dd-<reversed>
// back into the original model name used for routing and upstream requests.
func rewriteClaudeDDModelInBody(rawJSON []byte) []byte {
	modelName := gjson.GetBytes(rawJSON, "model").String()
	resolved := claudemodels.ResolveClaudeModelIDPrefix(modelName)
	if resolved == modelName {
		return rawJSON
	}
	updated, errSet := sjson.SetBytes(rawJSON, "model", resolved)
	if errSet != nil {
		return rawJSON
	}
	return updated
}

// ClaudeModels handles the Claude models listing endpoint.
// It returns a JSON response containing available Claude models and their specifications.
//
// Parameters:
//   - c: The Gin context for the request.
func (h *ClaudeCodeAPIHandler) ClaudeModels(c *gin.Context) {
	disableCloaking := h.Cfg != nil && h.Cfg.ClaudeCode.DisableCloakingModelList
	c.JSON(http.StatusOK, claudemodels.BuildResponse(h.ModelsForRequest(c, "claude", h.Models()), disableCloaking))
}

// handleNonStreamingResponse handles non-streaming content generation requests for Claude models.
// This function processes the request synchronously and returns the complete generated
// response in a single API call. It supports various generation parameters and
// response formats.
//
// Parameters:
//   - c: The Gin context for the request
//   - modelName: The model name to use for content generation
//   - rawJSON: The raw JSON request body containing generation parameters and content
func (h *ClaudeCodeAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")
	alt := h.GetAlt(c)
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	modelName := gjson.GetBytes(rawJSON, "model").String()

	// Claude responses must remain uncommitted until the upstream status and
	// request id are known. A flushed non-stream keepalive would lock in 200 and
	// the locally generated fallback request id, so the generic
	// nonstream-keepalive-interval setting intentionally does not apply here.
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, alt)
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}

	// Decompress gzipped responses - Claude API sometimes returns gzip without Content-Encoding header
	// This fixes title generation and other non-streaming responses that arrive compressed
	if len(resp) >= 2 && resp[0] == 0x1f && resp[1] == 0x8b {
		gzReader, errGzip := gzip.NewReader(bytes.NewReader(resp))
		if errGzip != nil {
			log.Warnf("failed to decompress gzipped Claude response: %v", errGzip)
		} else {
			defer func() {
				if errClose := gzReader.Close(); errClose != nil {
					log.Warnf("failed to close Claude gzip reader: %v", errClose)
				}
			}()
			decompressed, errRead := io.ReadAll(gzReader)
			if errRead != nil {
				log.Warnf("failed to read decompressed Claude response: %v", errRead)
			} else {
				resp = decompressed
			}
		}
	}

	writeClaudeUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse streams Claude-compatible responses.
// It sets up SSE, selects a backend client with rotation/quota logic,
// forwards chunks, and translates them to Claude CLI format.
//
// Parameters:
//   - c: The Gin context for the request.
//   - rawJSON: The raw JSON request body.
func (h *ClaudeCodeAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Get the http.Flusher interface to manually flush the response.
	// This is crucial for streaming as it allows immediate sending of data chunks
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		WriteProtocolError(c, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()

	// Create a cancellable context for the backend client request
	// This allows proper cleanup and cancellation of ongoing requests
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())

	dataChan, upstreamHeaders, errChan := h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}

	// Peek at the first chunk to determine success or failure before setting headers
	for {
		select {
		case <-c.Request.Context().Done():
			cliCancel(c.Request.Context().Err())
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			// Upstream failed immediately. Return proper error status and JSON.
			h.WriteErrorResponse(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				if errMsg, hasPendingError := handlers.PendingStreamError(errChan); hasPendingError {
					h.WriteErrorResponse(c, errMsg)
					if errMsg != nil {
						cliCancel(errMsg.Error)
					} else {
						cliCancel(nil)
					}
					return
				}
				// Validate even an empty stream; no terminal event means an upstream failure.
				setSSEHeaders()
				writeClaudeUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
				h.forwardClaudeStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan)
				return
			}

			// Success! Set headers now.
			setSSEHeaders()
			writeClaudeUpstreamHeaders(c.Writer.Header(), upstreamHeaders)

			// Write the first chunk and retain its SSE framing state.
			var streamState claudeSSEStreamState
			streamState.trailingNewlines = 2
			streamState.ObserveChunk(chunk)
			if len(chunk) > 0 {
				_, _ = c.Writer.Write(chunk)
				flusher.Flush()
			}

			// Continue streaming the rest
			h.forwardClaudeStreamWithState(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, &streamState)
			return
		}
	}
}

type claudeSSEStreamState struct {
	sawErrorEvent      bool
	sawMessageStop     bool
	sawBareMessageJSON bool
	trailingNewlines   int
	currentEvent       string
	eventData          [][]byte
	lineBuf            bytes.Buffer
}

func (s *claudeSSEStreamState) ObserveChunk(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	s.trailingNewlines = advanceClaudeSSENewlineCount(s.trailingNewlines, chunk)
	for _, b := range chunk {
		if b == '\n' {
			s.observeLine(s.lineBuf.Bytes())
			s.lineBuf.Reset()
			continue
		}
		s.lineBuf.WriteByte(b)
	}
}

func (s *claudeSSEStreamState) FlushPendingLine() {
	if s.lineBuf.Len() > 0 {
		s.observeLine(s.lineBuf.Bytes())
		s.lineBuf.Reset()
	}
	s.flushEventData()
}

func (s *claudeSSEStreamState) flushEventData() {
	if len(s.eventData) > 0 {
		joined := bytes.TrimSpace(bytes.Join(s.eventData, []byte("\n")))
		s.eventData = s.eventData[:0]
		if len(joined) > 0 && gjson.ValidBytes(joined) {
			s.observeJSONPayload(joined, true)
		}
	}
}

func (s *claudeSSEStreamState) ensureSSEEventBoundary(w io.Writer) {
	if s.sawBareMessageJSON {
		return
	}
	switch s.trailingNewlines {
	case 0:
		_, _ = w.Write([]byte("\n\n"))
	case 1:
		_, _ = w.Write([]byte("\n"))
	}
	s.trailingNewlines = 2
}

func (s *claudeSSEStreamState) observeLine(line []byte) {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		s.flushEventData()
		s.currentEvent = ""
		return
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		s.currentEvent = strings.TrimSpace(string(trimmed[len("event:"):]))
		return
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		payload := bytes.TrimSpace(trimmed[len("data:"):])
		if len(payload) > 0 {
			s.eventData = append(s.eventData, append([]byte(nil), payload...))
		}
		return
	}
	if !bytes.HasPrefix(trimmed, []byte("{")) || !gjson.ValidBytes(trimmed) {
		return
	}
	s.observeJSONPayload(trimmed, false)
}

func (s *claudeSSEStreamState) observeJSONPayload(payload []byte, hasDataPrefix bool) {
	root := gjson.ParseBytes(payload)
	switch root.Get("type").String() {
	case "message_stop":
		s.sawMessageStop = true
	case "error":
		s.sawErrorEvent = true
	case "message":
		if !hasDataPrefix && strings.TrimSpace(root.Get("stop_reason").String()) != "" {
			s.sawMessageStop = true
			s.sawBareMessageJSON = true
		}
	default:
		if hasDataPrefix && s.currentEvent == "error" && root.Get("error").Exists() {
			s.sawErrorEvent = true
		}
	}
}

func advanceClaudeSSENewlineCount(prev int, chunk []byte) int {
	count := prev
	for _, b := range chunk {
		switch b {
		case '\r':
			continue
		case '\n':
			if count < 2 {
				count++
			}
		default:
			count = 0
		}
	}
	return count
}

func (h *ClaudeCodeAPIHandler) forwardClaudeStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage) {
	state := &claudeSSEStreamState{
		trailingNewlines: 2,
	}
	h.forwardClaudeStreamWithState(c, flusher, cancel, data, errs, state)
}

func (h *ClaudeCodeAPIHandler) forwardClaudeStreamWithState(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, state *claudeSSEStreamState) {
	if state == nil {
		state = &claudeSSEStreamState{trailingNewlines: 2}
	}
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			if len(chunk) == 0 {
				return
			}
			state.ObserveChunk(chunk)
			_, _ = c.Writer.Write(chunk)
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			state.FlushPendingLine()
			if errMsg == nil {
				return
			}
			if state.sawErrorEvent {
				state.ensureSSEEventBoundary(c.Writer)
				return
			}
			state.ensureSSEEventBoundary(c.Writer)

			if !c.Writer.Written() {
				c.Status(claudeErrorStatus(errMsg))
			}
			errorBytes := h.claudeErrorBody(errMsg)
			_, _ = fmt.Fprintf(c.Writer, "event: error\ndata: %s\n\n", errorBytes)
		},
		CloseError: func() *interfaces.ErrorMessage {
			state.FlushPendingLine()
			if state.sawMessageStop || state.sawErrorEvent {
				return nil
			}
			return &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Error:      fmt.Errorf("upstream stream closed before message_stop"),
			}
		},
		WriteDone: func() {
			state.FlushPendingLine()
			if state.sawMessageStop || state.sawErrorEvent {
				state.ensureSSEEventBoundary(c.Writer)
			}
		},
	})
}

func claudeErrorStatus(msg *interfaces.ErrorMessage) int {
	status := http.StatusInternalServerError
	if msg != nil && msg.StatusCode > 0 {
		status = msg.StatusCode
	}
	if msg != nil && !msg.DirectResponse && coreauth.IsModelCooldownError(msg.Error) {
		if coreauth.IsQuotaCooldownError(msg.Error) {
			return http.StatusTooManyRequests
		}
		return claudeOverloadedStatus
	}
	return status
}

func (h *ClaudeCodeAPIHandler) toClaudeError(msg *interfaces.ErrorMessage) claudeErrorResponse {
	status := claudeErrorStatus(msg)
	errText := http.StatusText(status)
	if msg != nil && !msg.DirectResponse && coreauth.IsModelCooldownError(msg.Error) {
		if coreauth.IsQuotaCooldownError(msg.Error) {
			return claudeErrorResponse{
				Type: "error",
				Error: claudeErrorDetail{
					Type:    "rate_limit_error",
					Message: "Rate limit reached. Please try again later.",
				},
			}
		}
		return claudeErrorResponse{
			Type: "error",
			Error: claudeErrorDetail{
				Type:    "overloaded_error",
				Message: "Overloaded.",
			},
		}
	}
	if msg != nil && claudeAuthSelectionUnavailable(msg.Error) {
		return claudeErrorResponse{
			Type: "error",
			Error: claudeErrorDetail{
				Type:    "api_error",
				Message: "Service unavailable.",
			},
		}
	}
	if msg != nil {
		if msg.Error != nil {
			if v := strings.TrimSpace(msg.Error.Error()); v != "" {
				errText = v
			}
		}
	}
	errType, message := claudeErrorDetailFromText(status, errText)
	return claudeErrorResponse{
		Type: "error",
		Error: claudeErrorDetail{
			Type:    errType,
			Message: message,
		},
	}
}

func claudeAuthSelectionUnavailable(err error) bool {
	var authErr *coreauth.Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	code := strings.ToLower(strings.TrimSpace(authErr.Code))
	return code == "auth_not_found" || code == "auth_unavailable"
}

func (h *ClaudeCodeAPIHandler) WriteErrorResponse(c *gin.Context, msg *interfaces.ErrorMessage) {
	status := claudeErrorStatus(msg)
	if msg != nil && msg.DirectResponse {
		for key, values := range handlers.FilterUpstreamHeaders(msg.Headers) {
			if len(values) == 0 || handlers.IsCPAReservedResponseHeader(key) {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
		// A direct passthrough may already carry the upstream's request id;
		// only mint one when it does not.
		EnsureRequestID(c)
		clampClaudeThrottleRetryHeaders(c, status)
		body := h.claudeErrorBody(msg)
		appendClaudeAPIResponse(c, body)
		if !c.Writer.Written() && c.Writer.Header().Get("Content-Type") == "" {
			c.Writer.Header().Set("Content-Type", "application/json")
		}
		c.Status(status)
		_, _ = c.Writer.Write(body)
		return
	}
	passthroughHeaders := false
	if h != nil && h.BaseAPIHandler != nil {
		passthroughHeaders = handlers.PassthroughHeadersEnabled(h.Cfg)
	}
	if msg != nil && msg.Addon != nil && passthroughHeaders {
		for key, values := range msg.Addon {
			if len(values) == 0 || handlers.IsCPAReservedResponseHeader(key) {
				continue
			}
			c.Writer.Header().Del(key)
			for _, value := range values {
				c.Writer.Header().Add(key, value)
			}
		}
	}
	if msg != nil {
		writeClaudeRequestID(c.Writer.Header(), msg.Addon)
	}
	// The executor's unpadded upstream duration is authoritative over an addon
	// carrying the pool's padded cooldown value.
	if !writeClaudeDownstreamRetryAfter(c, status, msg) {
		// Copied header metadata is the fallback when no semantic duration exists.
		clampClaudeThrottleRetryHeaders(c, status)
	}
	writeClaudeProtocolErrorBody(c, status, h.claudeErrorBody(msg))
}

func writeClaudeDownstreamRetryAfter(c *gin.Context, status int, msg *interfaces.ErrorMessage) bool {
	if c == nil || msg == nil || msg.Error == nil || (status != http.StatusTooManyRequests && status != claudeOverloadedStatus) {
		return false
	}
	duration, ok := handlers.ResolveDownstreamRetryAfter(msg.Error)
	if !ok {
		return false
	}
	helps.SetClaudeDownstreamRetryAfterHeaders(c.Writer.Header(), duration)
	return true
}

func clampClaudeThrottleRetryHeaders(c *gin.Context, status int) {
	if c == nil || (status != http.StatusTooManyRequests && status != claudeOverloadedStatus) {
		return
	}
	helps.ClampClaudeDownstreamRetryAfterHeaders(c.Writer.Header(), time.Now())
}

func claudeErrorDetailFromText(status int, errText string) (string, string) {
	message := strings.TrimSpace(errText)
	if message == "" {
		message = http.StatusText(status)
	}
	errType := claudeErrorTypeFromStatus(status)

	var payload map[string]any
	if json.Valid([]byte(message)) {
		if err := json.Unmarshal([]byte(message), &payload); err == nil {
			if e, ok := payload["error"].(map[string]any); ok {
				if t, ok := e["type"].(string); ok && strings.TrimSpace(t) != "" {
					errType = strings.TrimSpace(t)
				}
				if m, ok := e["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				} else if c, ok := e["code"].(string); ok && strings.TrimSpace(c) != "" {
					message = strings.TrimSpace(c)
				}
			} else {
				if t, ok := payload["type"].(string); ok && strings.TrimSpace(t) != "" && strings.TrimSpace(t) != "error" {
					errType = strings.TrimSpace(t)
				}
				if m, ok := payload["message"].(string); ok && strings.TrimSpace(m) != "" {
					message = strings.TrimSpace(m)
				}
			}
		}
	}

	return errType, message
}

func appendClaudeAPIResponse(c *gin.Context, data []byte) {
	if c == nil || len(data) == 0 {
		return
	}
	if _, exists := c.Get("API_RESPONSE_TIMESTAMP"); !exists {
		c.Set("API_RESPONSE_TIMESTAMP", time.Now())
	}
	if existing, exists := c.Get("API_RESPONSE"); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+1)
			combined = append(combined, existingBytes...)
			if existingBytes[len(existingBytes)-1] != '\n' {
				combined = append(combined, '\n')
			}
			combined = append(combined, data...)
			c.Set("API_RESPONSE", combined)
			return
		}
	}
	c.Set("API_RESPONSE", bytes.Clone(data))
}

// claudeErrorBody adds the replay signal without discarding upstream fields.
func (h *ClaudeCodeAPIHandler) claudeErrorBody(msg *interfaces.ErrorMessage) []byte {
	var upstream []byte
	if msg != nil {
		upstream = clienterror.ErrorResponseBody(msg.Error)
		if msg.DirectResponse {
			upstream = msg.Body
		}
	}
	if clienterror.IsClaudeThreadNotFound(claudeErrorStatus(msg), errors.New(string(upstream))) {
		if marked, errMark := sjson.SetBytes(upstream, "error.details.error_code", "thread_not_found"); errMark == nil {
			var compact bytes.Buffer
			if errCompact := json.Compact(&compact, marked); errCompact == nil {
				return compact.Bytes()
			}
		}
	}
	if msg != nil && msg.DirectResponse {
		return bytes.Clone(msg.Body)
	}
	body, _ := json.Marshal(h.toClaudeError(msg))
	return body
}
