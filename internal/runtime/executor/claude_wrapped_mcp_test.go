package executor

import (
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

// The model sometimes wraps a caller-owned MCP tool in the request's virtual
// server, either by tool name (mcp__<virtual>__<tool>) or by its full name
// (mcp__<virtual>__mcp__<server>__<tool>). Both must restore to the declared tool.
func TestReverseRemapRestoresCallerMCPToolWrappedInVirtualServer(t *testing.T) {
	const recall = "mcp__docs-search__search-pages"
	body := []byte(`{"tools":[{"name":"Bash"},{"name":"` + recall + `"},{"name":"mcp__docs-search__search-issues"}]}`)
	_, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "wrapped-passthrough-caller"})
	var server string
	for alias, original := range reverseMap {
		if original == "Bash" {
			server = claudeMCPAliasServer(alias)
		}
	}
	if server == "" {
		t.Fatalf("no virtual server allocated: %v", reverseMap)
	}
	for _, wrapped := range []string{
		"mcp__" + server + "__search-pages",
		"mcp__" + server + "__" + recall,
	} {
		t.Run(wrapped, func(t *testing.T) {
			response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, wrapped))
			restored, errReverse := reverseRemapOAuthToolNames(response, reverseMap)
			if errReverse != nil {
				t.Fatalf("reverseRemapOAuthToolNames() error = %v", errReverse)
			}
			if got := gjson.GetBytes(restored, "content.0.name").String(); got != recall {
				t.Fatalf("wrapped caller MCP tool restored to %q, want %q", got, recall)
			}
			line := []byte(fmt.Sprintf(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}}`, wrapped))
			restoredLine, errStream := reverseRemapOAuthToolNamesFromStreamLine(line, reverseMap)
			if errStream != nil {
				t.Fatalf("reverseRemapOAuthToolNamesFromStreamLine() error = %v", errStream)
			}
			if got := gjson.GetBytes(helps.JSONPayload(restoredLine), "content_block.name").String(); got != recall {
				t.Fatalf("stream wrapped caller MCP tool restored to %q, want %q", got, recall)
			}
		})
	}
}

func TestReverseRemapWrappedCallerMCPToolStaysAnErrorWhenAmbiguous(t *testing.T) {
	body := []byte(`{"tools":[{"name":"Bash"},{"name":"mcp__a__query"},{"name":"mcp__b__query"}]}`)
	_, reverseMap := remapOAuthToolNamesWithOptions(body, claudeMCPAliasOptions{secret: "wrapped-ambiguous-caller"})
	var server string
	for alias, original := range reverseMap {
		if original == "Bash" {
			server = claudeMCPAliasServer(alias)
		}
	}
	response := []byte(fmt.Sprintf(`{"content":[{"type":"tool_use","id":"toolu_1","name":%q,"input":{}}]}`, "mcp__"+server+"__query"))
	if _, errReverse := reverseRemapOAuthToolNames(response, reverseMap); errReverse == nil || !strings.Contains(errReverse.Error(), "matches multiple declared tools") {
		t.Fatalf("ambiguous wrapped tool error = %v, want matches multiple declared tools", errReverse)
	}
}
