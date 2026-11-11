package common

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeFallbackModel returns a serving model explicitly reported by a fallback
// event. A different message_start model alone can be an alias or snapshot and
// does not establish that a fallback occurred.
func ClaudeFallbackModel(event gjson.Result) string {
	var usage gjson.Result
	switch event.Get("type").String() {
	case "content_block_start":
		block := event.Get("content_block")
		if block.Get("type").String() == "fallback" {
			if model := block.Get("to.model"); model.Type == gjson.String {
				return strings.TrimSpace(model.String())
			}
		}
	case "message_start":
		usage = event.Get("message.usage")
	case "message_delta":
		usage = event.Get("usage")
	}
	var model string
	iterations := usage.Get("iterations")
	if !iterations.IsArray() {
		return ""
	}
	for _, iteration := range iterations.Array() {
		if iteration.Get("type").String() == "fallback_message" {
			value := iteration.Get("model")
			if value.Type == gjson.String && strings.TrimSpace(value.String()) != "" {
				model = strings.TrimSpace(value.String())
			}
		}
	}
	return model
}
