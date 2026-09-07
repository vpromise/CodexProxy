package helps

import (
	"math"
	"net/http"
	"testing"
	"time"
)

func TestOpenAICompatRetryAfter(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		status  int
		headers http.Header
		body    string
		want    *time.Duration
	}{
		{
			name:    "zero delay",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {"0"}},
			want:    durationPointer(0),
		},
		{
			name:    "overflowing seconds saturate",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {"9223372036854775807"}},
			want:    durationPointer(time.Duration(math.MaxInt64)),
		},
		{
			name:    "past date is immediately retryable",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {now.Add(-time.Second).Format(http.TimeFormat)}},
			want:    durationPointer(0),
		},
		{
			name:    "negative seconds are invalid",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {"-5"}},
		},
		{
			name:    "delta seconds header",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {"17"}},
			want:    durationPointer(17 * time.Second),
		},
		{
			name:    "http date header",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {now.Add(23 * time.Second).Format(http.TimeFormat)}},
			want:    durationPointer(23 * time.Second),
		},
		{
			name:   "explicit TPM code fallback",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"ModelAccountTpmRateLimitExceeded","message":"TPM limit exceeded"}}`,
			want:   durationPointer(time.Minute),
		},
		{
			name:   "TPM message fallback",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"message":"TPM (Tokens Per Minute) limit of this model is exceeded"}}`,
			want:   durationPointer(time.Minute),
		},
		{
			name:    "provider header wins over fallback",
			status:  http.StatusTooManyRequests,
			headers: http.Header{"Retry-After": {"5"}},
			body:    `{"error":{"code":"ModelAccountTpmRateLimitExceeded"}}`,
			want:    durationPointer(5 * time.Second),
		},
		{
			name:   "generic 429 has no invented deadline",
			status: http.StatusTooManyRequests,
			body:   `{"error":{"code":"rate_limit"}}`,
		},
		{
			name:    "non-429 ignores header",
			status:  http.StatusServiceUnavailable,
			headers: http.Header{"Retry-After": {"30"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := OpenAICompatRetryAfter(test.status, test.headers, []byte(test.body), now)
			if test.want == nil {
				if got != nil {
					t.Fatalf("retry-after = %v, want nil", *got)
				}
				return
			}
			if got == nil || *got != *test.want {
				t.Fatalf("retry-after = %v, want %v", got, *test.want)
			}
		})
	}
}

func durationPointer(value time.Duration) *time.Duration {
	return &value
}
