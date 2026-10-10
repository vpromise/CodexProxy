package helps

import (
	"encoding/base64"
	"strconv"

	"github.com/tidwall/gjson"
)

func isPortableToolCallID(id string) bool {
	if id == "" {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '-' {
			continue
		}
		return false
	}
	return true
}

// NormalizeClaudeToolCallIDs repairs only client tool ID fields. Existing legal
// IDs (including reserved-prefix IDs) are never re-encoded. Legacy collisions get
// a deterministic underscore-prefixed alias, without a process-wide ledger.
func NormalizeClaudeToolCallIDs(body []byte) []byte {
	type idField struct {
		start int
		end   int
		id    string
	}
	var fields []idField
	used := make(map[string]bool)
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			field := ""
			switch block.Get("type").String() {
			case "tool_use":
				field = "id"
			case "tool_result":
				field = "tool_use_id"
			default:
				continue
			}
			value := block.Get(field)
			if value.Type != gjson.String || value.String() == "" {
				continue
			}
			id := value.String()
			if isPortableToolCallID(id) {
				used[id] = true
			} else {
				fields = append(fields, idField{start: value.Index, end: value.Index + len(value.Raw), id: id})
			}
		}
	}
	if len(fields) == 0 {
		return body
	}
	mapped := make(map[string]string)
	// Field offsets are in document order. Copy unchanged spans only once so
	// repairing large transcripts does not copy the whole payload per ID.
	out := make([]byte, 0, len(body))
	offset := 0
	for _, field := range fields {
		id := mapped[field.id]
		if id == "" {
			id = "cpa_tid_v1_" + base64.RawURLEncoding.EncodeToString([]byte(field.id))
			for used[id] {
				id = "_" + id
			}
			mapped[field.id] = id
			used[id] = true
		}
		out = append(out, body[offset:field.start]...)
		out = strconv.AppendQuote(out, id)
		offset = field.end
	}
	return append(out, body[offset:]...)
}
