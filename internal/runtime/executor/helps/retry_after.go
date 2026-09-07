package helps

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// RetryAfterSeconds converts a nonnegative retry delay without overflowing time.Duration.
func RetryAfterSeconds(seconds int64) time.Duration {
	if seconds <= 0 {
		return 0
	}
	if seconds > math.MaxInt64/int64(time.Second) {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(seconds) * time.Second
}

// OpenAICompatRetryAfter preserves the provider's standard Retry-After signal.
// Some OpenAI-compatible providers omit that header for explicit per-minute
// token limits; in that narrow case a one-minute fallback prevents immediate
// replay of the same large request while keeping the retry wait bounded.
func OpenAICompatRetryAfter(status int, headers http.Header, body []byte, now time.Time) *time.Duration {
	if status != http.StatusTooManyRequests {
		return nil
	}
	if raw := strings.TrimSpace(headers.Get("Retry-After")); raw != "" {
		if seconds, errParse := strconv.ParseInt(raw, 10, 64); errParse == nil && seconds >= 0 {
			delay := RetryAfterSeconds(seconds)
			return &delay
		}
		if deadline, errParse := http.ParseTime(raw); errParse == nil {
			delay := deadline.Sub(now)
			if delay < 0 {
				delay = 0
			}
			return &delay
		}
	}

	code := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.code").String()))
	message := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "error.message").String()))
	if strings.Contains(code, "tpmratelimitexceeded") ||
		(strings.Contains(message, "tokens per minute") && strings.Contains(message, "limit") && strings.Contains(message, "exceeded")) {
		delay := time.Minute
		return &delay
	}
	return nil
}
