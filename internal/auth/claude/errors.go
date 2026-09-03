// Package claude provides authentication and token management functionality
// for Anthropic's Claude AI services. It handles OAuth2 token storage, serialization,
// and retrieval for maintaining authenticated sessions with the Claude API.
package claude

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// OAuthHTTPStatusError preserves structured details from a rejected OAuth
// control-plane request without putting the response body in the error string.
type OAuthHTTPStatusError struct {
	Operation string
	Status    int
	ErrorType string
	Message   string
}

func (e *OAuthHTTPStatusError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s failed with status %d", strings.TrimSpace(e.Operation), e.Status)
}

// StatusCode returns the upstream HTTP status.
func (e *OAuthHTTPStatusError) StatusCode() int {
	if e == nil {
		return 0
	}
	return e.Status
}

// NewOAuthHTTPStatusError creates a typed OAuth control-plane error.
func NewOAuthHTTPStatusError(operation string, status int, errorType, message string) *OAuthHTTPStatusError {
	return &OAuthHTTPStatusError{
		Operation: strings.TrimSpace(operation),
		Status:    status,
		ErrorType: strings.TrimSpace(errorType),
		Message:   strings.TrimSpace(message),
	}
}

// IsOAuthProfileScopeError reports only explicit profile-scope rejections.
// A bare 403 is ambiguous: it may come from a proxy or an organization policy
// and must remain retryable.
func IsOAuthProfileScopeError(err error) bool {
	var statusErr *OAuthHTTPStatusError
	if !errors.As(err, &statusErr) || statusErr == nil || statusErr.Status != http.StatusForbidden {
		return false
	}
	errorType := strings.ToLower(strings.TrimSpace(statusErr.ErrorType))
	message := strings.ToLower(strings.TrimSpace(statusErr.Message))
	permissionError := errorType == "permission_error" || errorType == "insufficient_scope"
	scopeError := strings.Contains(message, "scope requirement") || strings.Contains(message, "insufficient_scope")
	profileScope := strings.Contains(message, "user:profile") || strings.Contains(message, "user:office")
	return permissionError && scopeError && profileScope
}

// OAuthError represents an OAuth-specific error.
type OAuthError struct {
	// Code is the OAuth error code.
	Code string `json:"error"`
	// Description is a human-readable description of the error.
	Description string `json:"error_description,omitempty"`
	// URI is a URI identifying a human-readable web page with information about the error.
	URI string `json:"error_uri,omitempty"`
	// StatusCode is the HTTP status code associated with the error.
	StatusCode int `json:"-"`
}

// Error returns a string representation of the OAuth error.
func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("OAuth error %s: %s", e.Code, e.Description)
	}
	return fmt.Sprintf("OAuth error: %s", e.Code)
}

// NewOAuthError creates a new OAuth error with the specified code, description, and status code.
func NewOAuthError(code, description string, statusCode int) *OAuthError {
	return &OAuthError{
		Code:        code,
		Description: description,
		StatusCode:  statusCode,
	}
}

// AuthenticationError represents authentication-related errors.
type AuthenticationError struct {
	// Type is the type of authentication error.
	Type string `json:"type"`
	// Message is a human-readable message describing the error.
	Message string `json:"message"`
	// Code is the HTTP status code associated with the error.
	Code int `json:"code"`
	// Cause is the underlying error that caused this authentication error.
	Cause error `json:"-"`
}

// Error returns a string representation of the authentication error.
func (e *AuthenticationError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s (caused by: %v)", e.Type, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Type, e.Message)
}

// Common authentication error types.
var (
	// ErrTokenExpired = &AuthenticationError{
	// 	Type:    "token_expired",
	// 	Message: "Access token has expired",
	// 	Code:    http.StatusUnauthorized,
	// }

	// ErrInvalidState represents an error for invalid OAuth state parameter.
	ErrInvalidState = &AuthenticationError{
		Type:    "invalid_state",
		Message: "OAuth state parameter is invalid",
		Code:    http.StatusBadRequest,
	}

	// ErrCodeExchangeFailed represents an error when exchanging authorization code for tokens fails.
	ErrCodeExchangeFailed = &AuthenticationError{
		Type:    "code_exchange_failed",
		Message: "Failed to exchange authorization code for tokens",
		Code:    http.StatusBadRequest,
	}

	// ErrServerStartFailed represents an error when starting the OAuth callback server fails.
	ErrServerStartFailed = &AuthenticationError{
		Type:    "server_start_failed",
		Message: "Failed to start OAuth callback server",
		Code:    http.StatusInternalServerError,
	}

	// ErrPortInUse represents an error when the OAuth callback port is already in use.
	ErrPortInUse = &AuthenticationError{
		Type:    "port_in_use",
		Message: "OAuth callback port is already in use",
		Code:    13, // Special exit code for port-in-use
	}

	// ErrCallbackTimeout represents an error when waiting for OAuth callback times out.
	ErrCallbackTimeout = &AuthenticationError{
		Type:    "callback_timeout",
		Message: "Timeout waiting for OAuth callback",
		Code:    http.StatusRequestTimeout,
	}
)

// NewAuthenticationError creates a new authentication error with a cause based on a base error.
func NewAuthenticationError(baseErr *AuthenticationError, cause error) *AuthenticationError {
	return &AuthenticationError{
		Type:    baseErr.Type,
		Message: baseErr.Message,
		Code:    baseErr.Code,
		Cause:   cause,
	}
}

// IsAuthenticationError checks if an error is an authentication error.
func IsAuthenticationError(err error) bool {
	var authenticationError *AuthenticationError
	ok := errors.As(err, &authenticationError)
	return ok
}

// IsOAuthError checks if an error is an OAuth error.
func IsOAuthError(err error) bool {
	var oAuthError *OAuthError
	ok := errors.As(err, &oAuthError)
	return ok
}

// GetUserFriendlyMessage returns a user-friendly error message based on the error type.
func GetUserFriendlyMessage(err error) string {
	switch {
	case IsAuthenticationError(err):
		var authErr *AuthenticationError
		errors.As(err, &authErr)
		switch authErr.Type {
		case "token_expired":
			return "Your authentication has expired. Please log in again."
		case "token_invalid":
			return "Your authentication is invalid. Please log in again."
		case "authentication_required":
			return "Please log in to continue."
		case "port_in_use":
			return "The required port is already in use. Please close any applications using port 3000 and try again."
		case "callback_timeout":
			return "Authentication timed out. Please try again."
		case "browser_open_failed":
			return "Could not open your browser automatically. Please copy and paste the URL manually."
		default:
			return "Authentication failed. Please try again."
		}
	case IsOAuthError(err):
		var oauthErr *OAuthError
		errors.As(err, &oauthErr)
		switch oauthErr.Code {
		case "access_denied":
			return "Authentication was cancelled or denied."
		case "invalid_request":
			return "Invalid authentication request. Please try again."
		case "server_error":
			return "Authentication server error. Please try again later."
		default:
			return fmt.Sprintf("Authentication failed: %s", oauthErr.Description)
		}
	default:
		return "An unexpected error occurred. Please try again."
	}
}
