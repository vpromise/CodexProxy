package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeBodyUsesAdvancedToolUse recognizes features requiring the advanced-tool-use beta.
// Plain inline tool declarations do not require this capability.
func ClaudeBodyUsesAdvancedToolUse(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if strings.HasPrefix(toolType, "tool_search_tool_") || tool.Get("defer_loading").Bool() ||
			tool.Get("input_examples").Exists() || tool.Get("allowed_callers").Exists() {
			return true
		}
	}
	return false
}
