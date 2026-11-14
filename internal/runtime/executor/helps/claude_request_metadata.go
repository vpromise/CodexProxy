package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type claudeUsedTool struct {
	Name string `json:"name"`
	MCP  bool   `json:"mcp,omitempty"`
}

// ApplyClaudeCodeRequestMetadata pairs request metadata with the finished CLI
// payload. Callers invoke it only for CLI-profile or confirmed native requests.
func ApplyClaudeCodeRequestMetadata(req *http.Request, body []byte, incoming http.Header, confirmedNative bool, reverseAliases map[string]string) {
	if req == nil || !IsAnthropicUpstreamURL(req.URL) {
		return
	}
	if confirmedNative {
		for _, name := range []string{
			"X-Claude-Code-Request-Class", "X-Claude-Code-Agent-Type",
			"X-Claude-Code-Prev-Tool-Durations", "X-Claude-Code-Compaction",
			"X-Claude-Code-Context-Compacted", "X-Claude-Code-Prompt-Id",
			"X-Claude-Code-Sandy-Plover",
		} {
			if value := HeaderValueCaseInsensitive(incoming, name); value != "" && HeaderValueCaseInsensitive(req.Header, name) == "" {
				req.Header.Set(name, value)
			}
		}
	}
	// CountTokens omits billing attribution. Preserve explicit native metadata
	// without synthesizing message-only fields on that endpoint.
	if strings.HasSuffix(req.URL.Path, "/count_tokens") {
		return
	}
	if _, promptID := ExtractClaudeBillingTags(body); promptID != "" {
		req.Header.Set("X-Claude-Code-Prompt-Id", promptID)
	}
	forwardAliases := make(map[string]string, len(reverseAliases))
	for alias, original := range reverseAliases {
		forwardAliases[original] = alias
	}
	if value := req.Header.Get("X-Claude-Code-Prev-Tool-Durations"); value != "" && len(forwardAliases) > 0 {
		parts := strings.Split(value, ",")
		for i, part := range parts {
			name, duration, ok := strings.Cut(part, "=")
			if alias := forwardAliases[strings.TrimSpace(name)]; ok && alias != "" {
				parts[i] = alias + "=" + duration
			}
		}
		req.Header.Set("X-Claude-Code-Prev-Tool-Durations", strings.Join(parts, ","))
	}
	value := req.Header.Get("X-Claude-Code-Sandy-Plover")
	if value != "" && len(forwardAliases) > 0 && (!gjson.Valid(value) || !gjson.Parse(value).IsArray()) {
		req.Header.Del("X-Claude-Code-Sandy-Plover")
		value = ""
	}
	if value != "" {
		// Preserve native connector metadata verbatim unless a tool was renamed.
		// A virtual MCP alias has no association with the original connector URL.
		if len(forwardAliases) > 0 {
			for i, tool := range gjson.Parse(value).Array() {
				if alias := forwardAliases[tool.Get("name").String()]; alias != "" && alias != tool.Get("name").String() {
					value, _ = sjson.Set(value, strconv.Itoa(i), claudeUsedTool{Name: alias, MCP: true})
				}
			}
			if len(value) > 7168 {
				req.Header.Del("X-Claude-Code-Sandy-Plover")
			} else {
				req.Header.Set("X-Claude-Code-Sandy-Plover", value)
			}
		}
		return
	}
	// Incremental Thread bodies do not contain the complete tool history. A
	// supplied native snapshot remains authoritative across those requests.
	if ClaudeThreadContinues(body) || IsClaudeProbeOrHelperRequest(body) {
		return
	}
	if value = claudeUsedToolsHeader(body); value != "" {
		req.Header.Set("X-Claude-Code-Sandy-Plover", value)
	}
}

// claudeUsedToolsHeader reconstructs the observed cumulative tool set from
// completed client tool calls in the supplied history, in first-use order.
func claudeUsedToolsHeader(body []byte) string {
	results := make(map[string]bool)
	messages := gjson.GetBytes(body, "messages").Array()
	for _, message := range messages {
		if message.Get("role").String() == "user" {
			for _, block := range message.Get("content").Array() {
				if block.Get("type").String() == "tool_result" {
					results[block.Get("tool_use_id").String()] = true
				}
			}
		}
	}
	tools := make([]claudeUsedTool, 0)
	seen := make(map[string]bool)
	for _, message := range messages {
		if message.Get("role").String() != "assistant" {
			continue
		}
		for _, block := range message.Get("content").Array() {
			name := block.Get("name").String()
			if block.Get("type").String() != "tool_use" || !results[block.Get("id").String()] || name == "" || seen[name] {
				continue
			}
			seen[name] = true
			tools = append(tools, claudeUsedTool{Name: name, MCP: IsClaudeMCPToolName(name)})
		}
	}
	// Native 2.1.321 caps this field at 64 entries / 7168 bytes and tool names
	// at 256 UTF-8 bytes. Extended/truncated connector summaries require a
	// native snapshot; do not manufacture an empty or partial history.
	for _, tool := range tools {
		if len(tool.Name) > 256 {
			return ""
		}
	}
	if len(tools) > 64 {
		return ""
	}
	// The 2.1.321 client places its core tools after other tool entries while
	// retaining first-use order within both groups.
	builtin := map[string]bool{
		"Bash": true, "Read": true, "Edit": true, "Write": true,
		"Glob": true, "Grep": true, "NotebookEdit": true, "TodoWrite": true,
		"Agent": true, "Task": true, "ToolSearch": true,
		"EnterPlanMode": true, "ExitPlanMode": true,
	}
	sort.SliceStable(tools, func(i, j int) bool { return !builtin[tools[i].Name] && builtin[tools[j].Name] })
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(tools); err != nil {
		return ""
	}
	var ascii strings.Builder
	for _, unit := range utf16.Encode([]rune(strings.TrimSuffix(encoded.String(), "\n"))) {
		if unit >= 0x7f {
			fmt.Fprintf(&ascii, `\u%04x`, unit)
		} else {
			ascii.WriteByte(byte(unit))
		}
	}
	if ascii.Len() > 7168 {
		return ""
	}
	return ascii.String()
}
