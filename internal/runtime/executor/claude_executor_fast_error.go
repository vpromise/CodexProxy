package executor

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// claudeFastRequestError marks a request-scoped Fast failure while preserving
// explicit credential-scoped classifications from the underlying error.
type claudeFastRequestError struct {
	cause         error
	status        int
	retryAfter    *time.Duration
	requestScoped bool
}

func (e *claudeFastRequestError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *claudeFastRequestError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *claudeFastRequestError) StatusCode() int {
	if e == nil {
		return 0
	}
	if e.status >= http.StatusMultipleChoices {
		return e.status
	}
	var statusProvider interface{ StatusCode() int }
	if errors.As(e.cause, &statusProvider) && statusProvider != nil {
		return statusProvider.StatusCode()
	}
	return 0
}

func (e *claudeFastRequestError) IsRequestScoped() bool {
	if e == nil {
		return false
	}
	return !e.IsCredentialScoped() && e.requestScoped
}

func (e *claudeFastRequestError) IsCredentialScoped() bool {
	if e == nil {
		return false
	}
	type credentialScopedProvider interface {
		IsCredentialScoped() bool
	}
	var csp credentialScopedProvider
	if errors.As(e.cause, &csp) && csp != nil {
		return csp.IsCredentialScoped()
	}
	return false
}

func (e *claudeFastRequestError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	return e.retryAfter
}

// claudeFastDirectResponseError carries an upstream HTTP error response through
// the auth manager and protocol handlers without rebuilding its status and JSON
// body. Credential-scoped errors may still be refreshed or retried by the manager.
type claudeFastDirectResponseError struct {
	response         *cliproxyexecutor.RequestTerminatedError
	retryAfter       *time.Duration
	credentialScoped bool
	requestScoped    bool
}

func (e *claudeFastDirectResponseError) Error() string {
	if e == nil || e.response == nil {
		return ""
	}
	return fmt.Sprintf("claude Fast upstream request failed with status %d", e.response.HTTPStatus)
}

func (e *claudeFastDirectResponseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.response
}

func (e *claudeFastDirectResponseError) IsRequestScoped() bool {
	if e == nil {
		return false
	}
	return !e.credentialScoped && e.requestScoped
}

func (e *claudeFastDirectResponseError) IsCredentialScoped() bool {
	if e == nil {
		return false
	}
	return e.credentialScoped
}

func (e *claudeFastDirectResponseError) RetryAfter() *time.Duration {
	if e == nil {
		return nil
	}
	return e.retryAfter
}

func wrapClaudeFastRequestError(fastRequest bool, status int, err error) error {
	if err == nil || !fastRequest {
		return err
	}
	var retryAfter *time.Duration
	var rap interface{ RetryAfter() *time.Duration }
	if errors.As(err, &rap) && rap != nil {
		retryAfter = rap.RetryAfter()
	}
	requestScoped, _ := claudeFastErrorScope(status, err)
	return &claudeFastRequestError{cause: err, status: status, retryAfter: retryAfter, requestScoped: requestScoped}
}

func newClaudeFastDirectResponseError(resp *http.Response, body []byte) error {
	if resp == nil {
		return nil
	}
	headers := resp.Header.Clone()
	// body has already been decoded. Do not forward stale representation or
	// length headers that describe the compressed upstream bytes.
	headers.Del("Content-Encoding")
	headers.Del("Content-Length")

	classified := classifyClaudeUpstreamError(resp.StatusCode, resp.Header, body)
	requestScoped, credentialScoped := claudeFastErrorScope(resp.StatusCode, classified)
	var retryAfter *time.Duration
	var rap interface{ RetryAfter() *time.Duration }
	if errors.As(classified, &rap) && rap != nil {
		retryAfter = rap.RetryAfter()
	}

	return &claudeFastDirectResponseError{
		response: &cliproxyexecutor.RequestTerminatedError{
			HTTPStatus: resp.StatusCode,
			Header:     headers,
			Body:       bytes.Clone(body),
		},
		retryAfter:       retryAfter,
		credentialScoped: credentialScoped,
		requestScoped:    requestScoped,
	}
}

func claudeFastErrorScope(status int, err error) (requestScoped, credentialScoped bool) {
	if err == nil {
		return false, false
	}
	type credentialScopedProvider interface {
		IsCredentialScoped() bool
	}
	var credentialProvider credentialScopedProvider
	if errors.As(err, &credentialProvider) && credentialProvider != nil && credentialProvider.IsCredentialScoped() {
		return false, true
	}
	var requestProvider cliproxyexecutor.RequestScopedError
	if errors.As(err, &requestProvider) && requestProvider != nil && requestProvider.IsRequestScoped() {
		return true, false
	}
	if status < http.StatusBadRequest || status > 599 {
		var statusProvider interface{ StatusCode() int }
		if errors.As(err, &statusProvider) && statusProvider != nil {
			status = statusProvider.StatusCode()
		}
	}
	switch status {
	case http.StatusBadRequest,
		http.StatusNotFound,
		http.StatusMethodNotAllowed,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnsupportedMediaType,
		http.StatusUnprocessableEntity:
		return true, false
	default:
		return false, false
	}
}

func claudeRequestIsFast(req *http.Request, body []byte) bool {
	if req == nil {
		return false
	}
	betas := strings.Join(req.Header.Values("Anthropic-Beta"), ",")
	return claudeRequestUsesFastMode(body, claudeRequestedBetas(betas, nil))
}

func claudeAuthLogIdentity(auth *cliproxyauth.Auth) (id, label, authType, authValue string) {
	if auth == nil {
		return "", "", "", ""
	}
	authType, authValue = auth.AccountInfo()
	return auth.ID, auth.Label, authType, authValue
}
