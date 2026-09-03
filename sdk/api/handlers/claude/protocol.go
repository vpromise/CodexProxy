package claude

import (
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

// RequestBodyMaxBytes limits both encoded and decoded Claude API request bodies.
const RequestBodyMaxBytes int64 = 32 << 20

const (
	claudeOverloadedStatus  = 529
	claudeRequestIDAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

type claudeErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type claudeErrorResponse struct {
	Type  string            `json:"type"`
	Error claudeErrorDetail `json:"error"`
}

// NewClaudeRequestID mints a proxy-originated request id in Anthropic's
// "req_" plus 26-character Crockford base32 shape. The first 10 characters
// encode the millisecond timestamp and the remaining 16 are random.
func NewClaudeRequestID() string {
	var encoded [26]byte
	milliseconds := uint64(time.Now().UnixMilli())
	for i := 9; i >= 0; i-- {
		encoded[i] = claudeRequestIDAlphabet[milliseconds&0x1f]
		milliseconds >>= 5
	}
	var entropy [16]byte
	_, _ = rand.Read(entropy[:])
	for i := range entropy {
		encoded[10+i] = claudeRequestIDAlphabet[entropy[i]&0x1f]
	}
	return "req_" + string(encoded[:])
}

// EnsureRequestID gives a Claude response a fallback request id. A real
// upstream request id may replace it before the response is committed.
func EnsureRequestID(c *gin.Context) {
	if c == nil {
		return
	}
	header := c.Writer.Header()
	if header.Get("Request-Id") == "" {
		header.Set("Request-Id", NewClaudeRequestID())
	}
}

func writeClaudeRequestID(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	if requestID := strings.TrimSpace(src.Get("Request-Id")); requestID != "" {
		dst.Set("Request-Id", requestID)
	}
}

// writeClaudeUpstreamHeaders applies the protocol-owned request id before the
// normal filtered response headers. Request-Id is preserved even when general
// upstream-header passthrough is disabled by the execution layer.
func writeClaudeUpstreamHeaders(dst, src http.Header) {
	writeClaudeRequestID(dst, src)
	handlers.WriteUpstreamHeaders(dst, src)
}

// WriteProtocolError renders a proxy-produced Claude error with one stable JSON
// shape and content type. Callers that are middleware must abort the Gin chain.
func WriteProtocolError(c *gin.Context, status int, message string) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = http.StatusText(status)
	}
	writeClaudeProtocolError(c, status, claudeErrorResponse{
		Type: "error",
		Error: claudeErrorDetail{
			Type:    claudeErrorTypeFromStatus(status),
			Message: message,
		},
	})
}

func writeClaudeProtocolError(c *gin.Context, status int, response claudeErrorResponse) {
	if c == nil {
		return
	}
	EnsureRequestID(c)
	body, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		body = []byte(`{"type":"error","error":{"type":"api_error","message":"Internal Server Error"}}`)
	}
	appendClaudeAPIResponse(c, body)
	if !c.Writer.Written() {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Status(status)
	_, _ = c.Writer.Write(body)
}

func claudeErrorTypeFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusPaymentRequired:
		return "billing_error"
	case http.StatusForbidden:
		return "permission_error"
	case http.StatusNotFound:
		return "not_found_error"
	case http.StatusRequestEntityTooLarge:
		return "request_too_large"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusGatewayTimeout:
		return "timeout_error"
	case claudeOverloadedStatus:
		return "overloaded_error"
	default:
		if status >= http.StatusInternalServerError {
			return "api_error"
		}
		return "invalid_request_error"
	}
}
