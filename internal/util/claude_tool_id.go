package util

import (
	"fmt"
	"regexp"
	"sync/atomic"
	"time"
)

var (
	claudeToolUseIDSanitizer = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	claudeToolUseIDCounter   uint64
)

// SanitizeClaudeToolID normalizes an ID to Claude's tool_use.id character set.
func SanitizeClaudeToolID(id string) string {
	sanitized := claudeToolUseIDSanitizer.ReplaceAllString(id, "_")
	if sanitized == "" {
		sanitized = fmt.Sprintf("toolu_%d_%d", time.Now().UnixNano(), atomic.AddUint64(&claudeToolUseIDCounter, 1))
	}
	return sanitized
}
