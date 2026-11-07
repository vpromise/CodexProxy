package helps

import (
	"math"
	"strconv"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeCodexOutputBudget bridges valid small translated budgets to Codex's minimum.
// Call this before payload rules so explicit operator overrides and filters remain final.
func NormalizeCodexOutputBudget(body []byte) []byte {
	budget := gjson.GetBytes(body, "max_output_tokens")
	if budget.Type != gjson.Number {
		return body
	}
	tokens, errParse := strconv.ParseFloat(budget.Raw, 64)
	if errParse != nil || tokens < 1 || tokens >= 16 || math.Trunc(tokens) != tokens {
		return body
	}
	normalized, errSet := sjson.SetBytes(body, "max_output_tokens", 16)
	if errSet == nil {
		return normalized
	}
	return body
}
