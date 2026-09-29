package logging

import (
	"context"
	"io"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestIDKey is the context key for storing/retrieving request IDs.
type requestIDKey struct{}

// ginRequestIDKey is the Gin context key for request IDs.
const ginRequestIDKey = "__request_id__"

// GenerateRequestID creates a complete UUIDv7 request ID.
func GenerateRequestID() (string, error) {
	return GenerateRequestIDFromReader(nil)
}

// GenerateRequestIDFromReader uses the default random source when reader is nil.
// Entropy failures return an error instead of a shared fallback request ID.
func GenerateRequestIDFromReader(reader io.Reader) (string, error) {
	var id uuid.UUID
	var errGenerate error
	if reader == nil {
		id, errGenerate = uuid.NewV7()
	} else {
		id, errGenerate = uuid.NewV7FromReader(reader)
	}
	if errGenerate != nil {
		return "", errGenerate
	}
	return id.String(), nil
}

// WithRequestID returns a new context with the request ID attached.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// GetRequestID retrieves the request ID from the context.
// Returns empty string if not found.
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// SetGinRequestID stores the request ID in the Gin context.
func SetGinRequestID(c *gin.Context, requestID string) {
	if c != nil {
		c.Set(ginRequestIDKey, requestID)
	}
}

// GetGinRequestID retrieves the request ID from the Gin context.
func GetGinRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if id, exists := c.Get(ginRequestIDKey); exists {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}
