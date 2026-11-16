package helps

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeClaudeAdvisorToolModels removes local thinking suffixes from each
// advisor's own model. Run before payload rules so explicit operator values win.
func NormalizeClaudeAdvisorToolModels(body []byte) []byte {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	type update struct{ path, model string }
	var updates []update
	tools.ForEach(func(index, tool gjson.Result) bool {
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(tool.Get("type").String())), "advisor_") {
			return true
		}
		value := tool.Get("model")
		if value.Type != gjson.String {
			return true
		}
		parsed := thinking.ParseSuffix(strings.TrimSpace(value.String()))
		model := strings.TrimSpace(parsed.ModelName)
		if parsed.HasSuffix && model != "" {
			updates = append(updates, update{fmt.Sprintf("tools.%d.model", index.Int()), model})
		}
		return true
	})
	for _, u := range updates {
		if next, errSet := sjson.SetBytes(body, u.path, u.model); errSet == nil {
			body = next
		}
	}
	return body
}
