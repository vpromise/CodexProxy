package helps

import "github.com/tidwall/gjson"

// ClaudeToolChangeNamePath returns the path, relative to a mid-conversation
// tool_addition or tool_removal block, of the tool name that must carry the
// same MCP alias as tools[]. A tool_reference names a tool declared in tools[];
// upstream rejects a reference to an undeclared name. A tool_addition can
// instead carry a tool_definition (inline-tools-2026-09-15) whose definition is
// a tools[] entry; redefining a declared custom tool replaces it only under the
// same upstream name. Server tool definitions and MCP connector references keep
// their names, matching the tools[] rewrite.
func ClaudeToolChangeNamePath(part gjson.Result) string {
	switch part.Get("tool.type").String() {
	case "tool_reference":
		return "tool.name"
	case "tool_definition":
		if part.Get("type").String() != "tool_addition" || IsClaudeServerToolType(part.Get("tool.definition.type").String()) {
			return ""
		}
		return "tool.definition.name"
	}
	return ""
}
