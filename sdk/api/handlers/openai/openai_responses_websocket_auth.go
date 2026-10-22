package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// authenticateResponsesWebsocketTurn rechecks frontend access before a new operation.
// It leaves the handshake and wire payload untouched and retains the existing pool checks.
func (h *OpenAIResponsesAPIHandler) authenticateResponsesWebsocketTurn(c *gin.Context, baseReq *http.Request, payload []byte, model string) *interfaces.ErrorMessage {
	if h == nil || h.AccessManager == nil || baseReq == nil {
		return nil
	}
	body := payload
	if model != "" && json.Valid(payload) && strings.TrimSpace(gjson.GetBytes(payload, "model").String()) == "" {
		if updated, err := sjson.SetBytes(bytes.Clone(payload), "model", model); err == nil {
			body = updated
		}
	}
	req := baseReq.Clone(baseReq.Context())
	req.Method = http.MethodPost
	for key := range req.Header {
		lower := strings.ToLower(key)
		if lower == "upgrade" || lower == "connection" || lower == "content-length" || strings.HasPrefix(lower, "sec-websocket-") {
			delete(req.Header, key)
		}
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	result, errAuth := h.AccessManager.Authenticate(req.Context(), req)
	if errAuth != nil {
		message := strings.TrimSpace(errAuth.Message)
		if message == "" {
			message = http.StatusText(errAuth.HTTPStatusCode())
		}
		if message == "" {
			message = "Authentication failed"
		}
		return &interfaces.ErrorMessage{StatusCode: errAuth.HTTPStatusCode(), Error: errors.New(message)}
	}
	principal, provider := "", ""
	var metadata map[string]string
	if result != nil {
		principal, provider, metadata = result.Principal, result.Provider, result.Metadata
	}
	c.Set("userApiKey", principal)
	c.Set("accessProvider", provider)
	c.Set("accessMetadata", metadata)
	ctx := coreauth.WithClientIdentity(c.Request.Context(), principal, provider)
	c.Request = c.Request.WithContext(ctx)
	return nil
}
