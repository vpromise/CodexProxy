package helps

import (
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeCodexClaudeOutputBudget bridges valid small Claude budgets to Codex's minimum.
// Call this before payload rules so explicit operator overrides and filters remain final.
func NormalizeCodexClaudeOutputBudget(body []byte, from string, payload []byte) []byte {
	if from != "claude" {
		return body
	}
	requested := gjson.GetBytes(payload, "max_tokens")
	if !requested.Exists() {
		requested = gjson.GetBytes(payload, "max_output_tokens")
	}
	if requested.Type != gjson.Number {
		return body
	}
	tokens, errParse := strconv.ParseInt(requested.Raw, 10, 64)
	if errParse != nil || tokens < 1 || tokens >= 16 {
		return body
	}
	translated := gjson.GetBytes(body, "max_output_tokens")
	if translated.Type == gjson.Number && translated.Int() > 0 && translated.Int() < 16 {
		normalized, errSet := sjson.SetBytes(body, "max_output_tokens", 16)
		if errSet == nil {
			return normalized
		}
	}
	return body
}
