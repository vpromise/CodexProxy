package helps

import "github.com/tidwall/gjson"

// ClaudeLeadingUserRunEnd skips directive-only system turns because Anthropic
// accepts them at any position. They must not cause caller system blocks to be
// inserted before a subsequent user turn.
func ClaudeLeadingUserRunEnd(messageBlocks []gjson.Result, firstUserIdx int) int {
	insertAt := firstUserIdx + 1
	for insertAt < len(messageBlocks) {
		message := messageBlocks[insertAt]
		if message.Get("role").String() != "user" && !isClaudeSystemDirectiveMessage(message) {
			break
		}
		insertAt++
	}
	return insertAt
}

func isClaudeSystemDirectiveMessage(message gjson.Result) bool {
	content := message.Get("content")
	return message.Get("role").String() == "system" &&
		message.Get("output_config").Exists() &&
		content.IsArray() && len(content.Array()) == 0
}
