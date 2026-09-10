package auth

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// logClaudeAllowedWarning records approaching limits without changing availability.
// It returns whether a warning was observed so stream completion can avoid logging it twice.
func logClaudeAllowedWarning(ctx context.Context, provider, model, authIndex string, headers http.Header) bool {
	if !strings.EqualFold(strings.TrimSpace(provider), "claude") || len(headers) == 0 {
		return false
	}
	windows := [...]struct {
		name        string
		header      string
		fieldPrefix string
	}{
		{"unified", "Anthropic-Ratelimit-Unified", "unified"},
		{"5h", "Anthropic-Ratelimit-Unified-5h", "five_hour"},
		{"7d", "Anthropic-Ratelimit-Unified-7d", "seven_day"},
	}
	var warningWindows []string
	for _, window := range windows {
		if strings.EqualFold(strings.TrimSpace(headers.Get(window.header+"-Status")), "allowed_warning") {
			warningWindows = append(warningWindows, window.name)
		}
	}
	if len(warningWindows) == 0 {
		return false
	}
	if !log.IsLevelEnabled(log.WarnLevel) {
		return true
	}
	fields := log.Fields{
		"provider":        "claude",
		"model":           model,
		"auth_index":      authIndex,
		"quota_status":    "allowed_warning",
		"warning_windows": strings.Join(warningWindows, ","),
	}
	// Only include parsed quota metrics from known headers, never raw response headers.
	for _, window := range windows {
		if used, errParse := strconv.ParseFloat(strings.TrimSpace(headers.Get(window.header+"-Utilization")), 64); errParse == nil && !math.IsNaN(used) && !math.IsInf(used, 0) && used >= 0 && used <= 1 {
			fields[window.fieldPrefix+"_used_percent"] = used * 100
		}
		if reset, errParse := strconv.ParseInt(strings.TrimSpace(headers.Get(window.header+"-Reset")), 10, 64); errParse == nil && reset > 0 {
			fields[window.fieldPrefix+"_reset_at"] = time.Unix(reset, 0).UTC().Format(time.RFC3339)
		}
	}
	logEntryWithRequestID(ctx).WithFields(fields).Warn("Claude quota is approaching a limit")
	return true
}
