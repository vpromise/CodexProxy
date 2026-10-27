package usage

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

type capturedAPIKeyContextKey struct{}

type capturedAPIKey struct {
	mu    sync.RWMutex
	value string
}

// WithCapturedAPIKey freezes the usage identity, including an empty key, in a
// separate holder. Derived contexts never mutate their parent's snapshot.
func WithCapturedAPIKey(ctx context.Context, key string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, capturedAPIKeyContextKey{}, &capturedAPIKey{value: key})
}

// CapturedAPIKeyFromContext returns the snapshot and whether one was captured.
// An explicitly empty snapshot must not fall back to a reused Gin context.
func CapturedAPIKeyFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	holder, _ := ctx.Value(capturedAPIKeyContextKey{}).(*capturedAPIKey)
	if holder == nil {
		return "", false
	}
	holder.mu.RLock()
	defer holder.mu.RUnlock()
	return holder.value, true
}

// CaptureAPIKey snapshots the request's usage identity while its Gin context is
// still owned by the handler. Direct SDK calls without Gin capture an empty key.
func CaptureAPIKey(ctx context.Context) context.Context {
	if _, exists := CapturedAPIKeyFromContext(ctx); exists {
		return ctx
	}
	key := ""
	if ctx != nil {
		getter, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) })
		if ok && getter != nil {
			value := reflect.ValueOf(getter)
			if value.Kind() != reflect.Pointer || !value.IsNil() {
				if raw, exists := getter.Get("userApiKey"); exists {
					switch v := raw.(type) {
					case string:
						key = v
					case fmt.Stringer:
						key = v.String()
					default:
						key = fmt.Sprintf("%v", v)
					}
				}
			}
		}
	}
	return WithCapturedAPIKey(ctx, key)
}

// SetCapturedAPIKey updates the request-local holder when Home resolves its
// existing usage identity. It does not change access grants or account selection.
func SetCapturedAPIKey(ctx context.Context, key string) {
	if ctx == nil {
		return
	}
	holder, _ := ctx.Value(capturedAPIKeyContextKey{}).(*capturedAPIKey)
	if holder == nil {
		return
	}
	holder.mu.Lock()
	holder.value = key
	holder.mu.Unlock()
}
