package helps

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParseClaudeRateLimitReset_OverageBillingBoundary(t *testing.T) {
	now := time.Date(2026, time.October, 9, 0, 0, 0, 0, time.UTC)
	billing := time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC)
	billingUnix := strconv.FormatInt(billing.Unix(), 10)
	tests := []struct {
		name    string
		headers map[string]string
		want    time.Duration
	}{
		{name: "matching billing reset falls back"},
		{name: "equivalent timezone and Unix timestamps", headers: map[string]string{"Anthropic-Ratelimit-Unified-Overage-Reset": "2026-11-01T08:00:00+08:00"}},
		{name: "normalized overage claim", headers: map[string]string{"Anthropic-Ratelimit-Unified-Representative-Claim": " OverAge "}},
		{name: "short retry survives", headers: map[string]string{"Retry-After": "60"}, want: time.Minute},
		{name: "explicit long retry survives", headers: map[string]string{"Retry-After": "777600"}, want: 9 * 24 * time.Hour},
		{name: "long millisecond retry keeps precedence", headers: map[string]string{"Retry-After-Ms": "691200000", "Retry-After": "60"}, want: 8 * 24 * time.Hour},
		{name: "shared window deadlines survive", headers: map[string]string{
			"Anthropic-Ratelimit-Unified-5h-Status": "rejected",
			"Anthropic-Ratelimit-Unified-5h-Reset":  strconv.FormatInt(now.Add(2*time.Hour).Unix(), 10),
			"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
			"Anthropic-Ratelimit-Unified-7d-Reset":  strconv.FormatInt(now.Add(4*24*time.Hour).Unix(), 10),
			"Retry-After":                           "60",
		}, want: 4 * 24 * time.Hour},
		{name: "matching explicit shared reset is retained", headers: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
			"Anthropic-Ratelimit-Unified-7d-Reset":  billingUnix,
		}, want: billing.Sub(now)},
		{name: "nonmatching reset is retained", headers: map[string]string{"Anthropic-Ratelimit-Unified-Overage-Reset": strconv.FormatInt(billing.Add(time.Second).Unix(), 10)}, want: billing.Sub(now)},
		{name: "missing billing reset is retained", headers: map[string]string{"Anthropic-Ratelimit-Unified-Overage-Reset": ""}, want: billing.Sub(now)},
		{name: "invalid billing reset is retained", headers: map[string]string{"Anthropic-Ratelimit-Unified-Overage-Reset": "not-a-time"}, want: billing.Sub(now)},
		{name: "missing claim is retained", headers: map[string]string{"Anthropic-Ratelimit-Unified-Representative-Claim": ""}, want: billing.Sub(now)},
		{name: "other claim is retained", headers: map[string]string{"Anthropic-Ratelimit-Unified-Representative-Claim": "seven_day"}, want: billing.Sub(now)},
		{name: "overage status alone is insufficient", headers: map[string]string{
			"Anthropic-Ratelimit-Unified-Representative-Claim": "",
			"Anthropic-Ratelimit-Unified-Overage-Status":       "rejected",
		}, want: billing.Sub(now)},
		{name: "invalid unified reset keeps retry", headers: map[string]string{"Anthropic-Ratelimit-Unified-Reset": "not-a-time", "Retry-After": "60"}, want: time.Minute},
		{name: "missing unified reset keeps retry", headers: map[string]string{"Anthropic-Ratelimit-Unified-Reset": "", "Retry-After": "60"}, want: time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			headers := make(http.Header)
			headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
			headers.Set("Anthropic-Ratelimit-Unified-Representative-Claim", "overage")
			headers.Set("Anthropic-Ratelimit-Unified-Reset", billingUnix)
			headers.Set("Anthropic-Ratelimit-Unified-Overage-Reset", billingUnix)
			for key, value := range tc.headers {
				if value == "" {
					headers.Del(key)
				} else {
					headers.Set(key, value)
				}
			}
			got := parseClaudeRateLimitResetWithFuzz(headers, now, 0, 0)
			if tc.want == 0 {
				if got != nil {
					t.Fatalf("cooldown = %v, want nil for generic backoff", *got)
				}
			} else if got == nil || *got != tc.want {
				t.Fatalf("cooldown = %v, want %v", got, tc.want)
			}
			if !ClaudeHeadersIndicateUnifiedRateLimitRejection(headers) {
				t.Fatal("billing filtering changed conservative credential-scoped classification")
			}
		})
	}
}
