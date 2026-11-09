package common

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// extractResponsesCallID extracts the tool call ID from a Responses API item,
// prioritizing dedicated tool call references over generic item IDs:
// call_id -> tool_call_id -> callId -> id (excluding fco_ output item IDs)
func extractResponsesCallID(node gjson.Result) string {
	if callID := strings.TrimSpace(node.Get("call_id").String()); callID != "" {
		return callID
	}
	if toolCallID := strings.TrimSpace(node.Get("tool_call_id").String()); toolCallID != "" {
		return toolCallID
	}
	if callId := strings.TrimSpace(node.Get("callId").String()); callId != "" {
		return callId
	}
	id := strings.TrimSpace(node.Get("id").String())
	if strings.HasPrefix(id, "fco_") {
		return ""
	}
	return id
}

// normalizeResponsesToolCallOutputs scans a slice of Responses input items,
// pairs function_call_output / custom_tool_call_output with preceding pending tool calls,
// and assigns missing call_ids to tool outputs using a multi-pass matching strategy:
// 1. Exact explicit call ID match (reserving calls for outputs that explicitly reference them across the entire conversation).
// 2. Function name match for outputs that omit a call ID (skipping pending calls reserved by future explicit outputs).
// 3. FIFO queue fallback for remaining outputs that omit a call ID (skipping pending calls reserved by future explicit outputs).
// Outputs that already carry an explicit call ID that does not match any pending call
// are never rewritten or stolen.
func normalizeResponsesToolCallOutputs(items []gjson.Result) []gjson.Result {
	if len(items) == 0 {
		return items
	}

	normalized := make([]gjson.Result, len(items))
	copy(normalized, items)

	explicitOutputCounts := make(map[string]int)
	for _, item := range items {
		typ := item.Get("type").String()
		if typ == "function_call_output" || typ == "custom_tool_call_output" {
			if id := extractResponsesCallID(item); id != "" {
				explicitOutputCounts[id]++
			}
		}
	}

	var pendingCallIDs []string
	pendingCallNames := make(map[string]string)
	pendingCallTypes := make(map[string]string)

	i := 0
	for i < len(normalized) {
		item := normalized[i]
		itemType := item.Get("type").String()

		switch itemType {
		case "function_call", "custom_tool_call":
			callID := extractResponsesCallID(item)
			if callID != "" {
				pendingCallIDs = append(pendingCallIDs, callID)
				name := item.Get("name").String()
				pendingCallNames[callID] = name
				pendingCallTypes[callID] = itemType + "_output"
			}
			i++

		case "function_call_output", "custom_tool_call_output":
			start := i
			for i < len(normalized) && (normalized[i].Get("type").String() == "function_call_output" || normalized[i].Get("type").String() == "custom_tool_call_output") {
				i++
			}
			outputs := normalized[start:i]

			if len(pendingCallIDs) > 0 {
				used := make([]bool, len(outputs))
				matchedForPending := make([]int, len(pendingCallIDs))
				for idx := range matchedForPending {
					matchedForPending[idx] = -1
				}

				// Pass 1: exact explicit call ID match
				for pendingIdx, pendingID := range pendingCallIDs {
					for outIdx, out := range outputs {
						if !used[outIdx] && out.Get("type").String() == pendingCallTypes[pendingID] && extractResponsesCallID(out) == pendingID {
							used[outIdx] = true
							matchedForPending[pendingIdx] = outIdx
							explicitOutputCounts[pendingID]--
							break
						}
					}
				}

				// Pass 2: match by function name for outputs with no explicit call ID (skipping pending IDs reserved by future explicit outputs)
				for pendingIdx, pendingID := range pendingCallIDs {
					if matchedForPending[pendingIdx] >= 0 || explicitOutputCounts[pendingID] > 0 {
						continue
					}
					expectedName := pendingCallNames[pendingID]
					if expectedName != "" {
						for outIdx, out := range outputs {
							if !used[outIdx] && out.Get("type").String() == pendingCallTypes[pendingID] && extractResponsesCallID(out) == "" && !isCodexDelegationToolOutput(out) {
								outName := strings.TrimSpace(out.Get("name").String())
								if outName != "" && outName == expectedName {
									used[outIdx] = true
									matchedForPending[pendingIdx] = outIdx
									break
								}
							}
						}
					}
				}

				// Pass 3: FIFO fallback for outputs with no explicit call ID (skipping pending IDs reserved by future explicit outputs)
				for pendingIdx := range pendingCallIDs {
					pendingID := pendingCallIDs[pendingIdx]
					if matchedForPending[pendingIdx] >= 0 || explicitOutputCounts[pendingID] > 0 {
						continue
					}
					for outIdx, out := range outputs {
						if !used[outIdx] && out.Get("type").String() == pendingCallTypes[pendingID] && extractResponsesCallID(out) == "" && !isCodexDelegationToolOutput(out) {
							outName := strings.TrimSpace(out.Get("name").String())
							expectedName := pendingCallNames[pendingID]
							if outName == "" || expectedName == "" || outName == expectedName {
								used[outIdx] = true
								matchedForPending[pendingIdx] = outIdx
								break
							}
						}
					}
				}

				// Apply matched call_ids to outputs
				var remainingPending []string
				for pendingIdx, pendingID := range pendingCallIDs {
					outIdx := matchedForPending[pendingIdx]
					if outIdx < 0 {
						remainingPending = append(remainingPending, pendingID)
						continue
					}
					matchedOut := outputs[outIdx]
					if matchedOut.Get("call_id").String() != pendingID {
						raw := []byte(matchedOut.Raw)
						raw, _ = sjson.SetBytes(raw, "call_id", pendingID)
						normalized[start+outIdx] = gjson.ParseBytes(raw)
					}
				}
				pendingCallIDs = remainingPending
			}

		default:
			i++
		}
	}

	return normalized
}

// SanitizeOrphanResponsesToolOutputs preserves context sent as a standalone tool
// result without a call reference, which strict Responses upstreams reject.
// Resolve pending calls first; explicit references and Codex delegation stay tools.
func SanitizeOrphanResponsesToolOutputs(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	original := input.Array()
	normalized := normalizeResponsesToolCallOutputs(original)
	rebuilt := make([][]byte, 0, len(original))
	pendingMissingIDType := ""
	changed := false
	for i, item := range normalized {
		raw := []byte(item.Raw)
		typ := strings.TrimSpace(item.Get("type").String())
		switch typ {
		case "function_call", "custom_tool_call":
			pendingMissingIDType = ""
			if extractResponsesCallID(item) == "" {
				pendingMissingIDType = typ + "_output"
			}
		case "function_call_output", "custom_tool_call_output":
			if isCodexDelegationToolOutput(item) {
				pendingMissingIDType = ""
				break
			}
			if id := responsesToolOutputExplicitCallID(item); id != "" {
				if item.Get("call_id").String() != id {
					if updated, errSet := sjson.SetBytes(raw, "call_id", id); errSet == nil {
						raw = updated
					}
				}
			} else if !(pendingMissingIDType == typ && strings.TrimSpace(item.Get("name").String()) == "" && strings.TrimSpace(item.Get("id").String()) == "") {
				raw = orphanResponsesToolOutputMessage(item)
			}
			pendingMissingIDType = ""
		default:
			pendingMissingIDType = ""
		}
		changed = changed || !bytes.Equal(raw, []byte(original[i].Raw))
		rebuilt = append(rebuilt, raw)
	}
	if !changed {
		return payload
	}
	result, errSet := sjson.SetRawBytes(payload, "input", JoinRawArray(rebuilt))
	if errSet != nil {
		return payload
	}
	return result
}

func responsesToolOutputExplicitCallID(item gjson.Result) string {
	for _, key := range []string{"call_id", "tool_call_id", "callId"} {
		value := item.Get(key)
		if value.Type == gjson.String {
			if id := strings.TrimSpace(value.String()); id != "" {
				return id
			}
		}
	}
	return ""
}

func isCodexDelegationToolOutput(item gjson.Result) bool {
	if strings.TrimSpace(item.Get("type").String()) != "function_call_output" || strings.TrimSpace(item.Get("namespace").String()) != "codex_app" {
		return false
	}
	switch strings.TrimSpace(item.Get("name").String()) {
	case "create_thread", "send_message_to_thread":
		return true
	default:
		return false
	}
}

func orphanResponsesToolOutputMessage(item gjson.Result) []byte {
	name := strings.TrimSpace(item.Get("name").String())
	if name == "" {
		name = "tool"
	}
	var text strings.Builder
	text.WriteString("Tool output from " + name + ":\n")
	var content [][]byte
	flush := func() {
		if text.Len() == 0 {
			return
		}
		part := []byte(`{"type":"input_text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", text.String())
		content = append(content, part)
		text.Reset()
	}
	output := item.Get("output")
	switch {
	case !output.Exists() || output.Type == gjson.Null:
		text.WriteString("(empty)")
	case output.Type == gjson.String:
		text.WriteString(output.String())
	case output.IsArray():
		parts := output.Array()
		for _, part := range parts {
			value := ""
			isText := part.Type == gjson.String
			if isText {
				value = part.String()
			}
			if part.IsObject() {
				typ := part.Get("type").String()
				if typ == "" || typ == "text" || typ == "input_text" || typ == "output_text" {
					for _, key := range []string{"text", "input_text", "output_text"} {
						if v := part.Get(key); v.Type == gjson.String {
							value = v.String()
							isText = true
							break
						}
					}
				}
			}
			if isText {
				if text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
					text.WriteByte('\n')
				}
				text.WriteString(value)
			} else if part.IsObject() {
				// Preserve all media/unknown typed parts, their raw numbers and source order.
				flush()
				content = append(content, []byte(part.Raw))
			} else {
				if text.Len() > 0 && !strings.HasSuffix(text.String(), "\n") {
					text.WriteByte('\n')
				}
				text.WriteString(part.Raw)
			}
		}
		if len(parts) == 0 {
			text.WriteString(output.Raw)
		}
	default:
		text.WriteString(output.Raw)
	}
	flush()
	message := []byte(`{"type":"message","role":"user","content":[]}`)
	message, _ = sjson.SetRawBytes(message, "content", JoinRawArray(content))
	return message
}
