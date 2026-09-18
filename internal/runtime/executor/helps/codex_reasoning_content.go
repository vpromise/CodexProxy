package helps

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeCodexReasoningContent preserves replayed cleartext reasoning in summary.
// Codex accepts an empty reasoning.content array; other Responses backends may
// accept cleartext content, so this normalization belongs only on Codex requests.
func NormalizeCodexReasoningContent(body []byte) []byte {
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	changed := false
	for i, item := range items {
		content := item.Get("content")
		if item.Get("type").String() != "reasoning" || !content.IsArray() || len(content.Array()) == 0 {
			continue
		}
		summary := item.Get("summary")
		if summary.Exists() && summary.Type != gjson.Null && !summary.IsArray() {
			continue
		}
		parts := summary.Array()
		existingText := make(map[string]bool, len(parts))
		for _, part := range parts {
			if part.Get("type").String() == "summary_text" {
				existingText[part.Get("text").String()] = true
			}
		}
		knownContent := true
		for _, part := range content.Array() {
			if part.Get("type").String() != "reasoning_text" || part.Get("text").Type != gjson.String {
				knownContent = false
				break
			}
			text := part.Get("text").String()
			if text == "" || existingText[text] {
				continue
			}
			partJSON, errSet := sjson.Set(`{"type":"summary_text"}`, "text", text)
			if errSet != nil {
				return body
			}
			parts = append(parts, gjson.Parse(partJSON))
		}
		// Leave unknown input shapes to upstream validation instead of dropping
		// content that a future protocol may give a different meaning.
		if !knownContent {
			continue
		}
		array := []byte{'['}
		for j, part := range parts {
			if j > 0 {
				array = append(array, ',')
			}
			array = append(array, part.Raw...)
		}
		array = append(array, ']')
		next, errSet := sjson.SetRaw(item.Raw, "summary", string(array))
		if errSet != nil {
			return body
		}
		next, errSet = sjson.SetRaw(next, "content", "[]")
		if errSet != nil {
			return body
		}
		items[i] = gjson.Parse(next)
		changed = true
	}
	if !changed {
		return body
	}
	array := make([]byte, 0, len(input.Raw))
	array = append(array, '[')
	for i, item := range items {
		if i > 0 {
			array = append(array, ',')
		}
		array = append(array, item.Raw...)
	}
	array = append(array, ']')
	updated, errSet := sjson.SetRawBytes(body, "input", array)
	if errSet != nil {
		return body
	}
	return updated
}
