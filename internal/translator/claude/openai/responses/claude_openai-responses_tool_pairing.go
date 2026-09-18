package responses

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// repairClaudeToolPairing enforces the Anthropic invariant that every
// assistant tool_use is answered by a tool_result at the start of the very
// next user message, and that every tool_result references a tool_use in the
// immediately preceding assistant message. Histories break it in practice
// when a session dies while a tool runs (tool_use with no output), when
// standalone context is injected ahead of a real tool output (text before
// tool_result), or when a delayed output lands after an intervening
// assistant message. Missing results are synthesized as errors and orphan
// results fold into plain text so the model can carry on.
func repairClaudeToolPairing(messages [][]byte) [][]byte {
	if len(messages) == 0 {
		return messages
	}
	messages = append([][]byte(nil), messages...)
	prevToolUseIDs := map[string]struct{}{}
	out := make([][]byte, 0, len(messages)+1)
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		role := gjson.GetBytes(msg, "role").String()
		content := gjson.GetBytes(msg, "content")

		if role == "user" {
			// Fold tool_result blocks that do not answer a tool_use in the
			// immediately preceding assistant message into plain text, and move
			// the real ones ahead of any other content.
			rebuilt, changed := normalizeClaudeToolResultMessage(msg, prevToolUseIDs)
			if changed {
				msg = rebuilt
				content = gjson.GetBytes(msg, "content")
			}
		}

		out = append(out, msg)

		prevToolUseIDs = map[string]struct{}{}
		if role == "assistant" {
			var toolUseIDs []string
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_use" {
					if id := block.Get("id").String(); id != "" {
						toolUseIDs = append(toolUseIDs, id)
						prevToolUseIDs[id] = struct{}{}
					}
				}
				return true
			})
			if len(toolUseIDs) == 0 {
				continue
			}

			hasNextUser := i+1 < len(messages) && gjson.GetBytes(messages[i+1], "role").String() == "user"
			answered := map[string]struct{}{}
			if hasNextUser {
				gjson.GetBytes(messages[i+1], "content").ForEach(func(_, block gjson.Result) bool {
					if block.Get("type").String() == "tool_result" {
						answered[block.Get("tool_use_id").String()] = struct{}{}
					}
					return true
				})
			}

			var synthesized [][]byte
			for _, id := range toolUseIDs {
				if _, ok := answered[id]; ok {
					continue
				}
				part := []byte(`{"type":"tool_result","tool_use_id":"","is_error":true,"content":"Tool call was interrupted before any output was recorded."}`)
				part, _ = sjson.SetBytes(part, "tool_use_id", id)
				synthesized = append(synthesized, part)
			}
			if len(synthesized) == 0 {
				continue
			}

			if hasNextUser {
				// Prepend the missing results so tool_result blocks still lead
				// the existing user message.
				next := messages[i+1]
				nextContent := gjson.GetBytes(next, "content")
				parts := synthesized
				if nextContent.IsArray() {
					nextContent.ForEach(func(_, block gjson.Result) bool {
						parts = append(parts, []byte(block.Raw))
						return true
					})
				} else if nextContent.Type == gjson.String {
					textPart := []byte(`{"type":"text","text":""}`)
					textPart, _ = sjson.SetBytes(textPart, "text", nextContent.String())
					parts = append(parts, textPart)
				}
				userMsg := next
				userMsg, _ = sjson.SetRawBytes(userMsg, "content", common.JoinRawArray(parts))
				messages[i+1] = userMsg
			} else {
				userMsg := []byte(`{"role":"user","content":[]}`)
				userMsg, _ = sjson.SetRawBytes(userMsg, "content", common.JoinRawArray(synthesized))
				out = append(out, userMsg)
			}
		}
	}
	return out
}

// normalizeClaudeToolResultMessage rebuilds a user message so tool_result
// blocks lead the content and any tool_result that does not answer a tool_use
// in the immediately preceding assistant message folds into plain text.
// answeredIDs lists the ids allowed to stay tool_result blocks.
func normalizeClaudeToolResultMessage(msg []byte, answeredIDs map[string]struct{}) ([]byte, bool) {
	content := gjson.GetBytes(msg, "content")
	if !content.IsArray() {
		return msg, false
	}
	var resultParts, otherParts [][]byte
	seenOther := false
	changed := false
	content.ForEach(func(_, block gjson.Result) bool {
		raw := []byte(block.Raw)
		if block.Get("type").String() == "tool_result" {
			if _, ok := answeredIDs[block.Get("tool_use_id").String()]; !ok {
				changed = true
				seenOther = true
				if textParts := toolResultTextParts(block); len(textParts) > 0 {
					otherParts = append(otherParts, textParts...)
				} else {
					// An empty orphan result folds to nothing, so keep an
					// explicit marker instead of producing an empty user
					// message that Anthropic also rejects.
					otherParts = append(otherParts, []byte(`{"type":"text","text":"Tool result was empty."}`))
				}
				return true
			}
			resultParts = append(resultParts, raw)
			if seenOther {
				changed = true
			}
			return true
		}
		seenOther = true
		otherParts = append(otherParts, raw)
		return true
	})
	if !changed {
		return msg, false
	}
	parts := make([][]byte, 0, len(resultParts)+len(otherParts))
	parts = append(parts, resultParts...)
	parts = append(parts, otherParts...)
	userMsg := msg
	userMsg, _ = sjson.SetRawBytes(userMsg, "content", common.JoinRawArray(parts))
	return userMsg, true
}

// toolResultTextParts folds an orphan tool_result block into plain text parts
// so it can ride along as ordinary user content. Empty text parts are
// filtered out; when nothing visible remains it returns nil so the caller
// can substitute a marker instead of emitting empty text blocks that
// Anthropic rejects.
func toolResultTextParts(block gjson.Result) [][]byte {
	content := block.Get("content")
	if content.IsArray() {
		var parts [][]byte
		content.ForEach(func(_, part gjson.Result) bool {
			raw := []byte(part.Raw)
			if part.Get("type").String() == "" {
				raw, _ = sjson.SetBytes(raw, "type", "text")
			}
			if !contentPartHasVisibleContent(raw) {
				return true
			}
			parts = append(parts, raw)
			return true
		})
		if len(parts) > 0 {
			return parts
		}
		return nil
	}
	text := content.String()
	if strings.TrimSpace(text) == "" {
		return nil
	}
	part := []byte(`{"type":"text","text":""}`)
	part, _ = sjson.SetBytes(part, "text", text)
	return [][]byte{part}
}

// convertResponsesStandaloneToolOutputToClaudeText renders a tool output that
// has no matching tool_use as ordinary user content blocks.
func convertResponsesStandaloneToolOutputToClaudeText(output gjson.Result) [][]byte {
	if output.Exists() && output.IsArray() {
		var partsJSON [][]byte
		output.ForEach(func(_, part gjson.Result) bool {
			if partJSON := convertResponsesContentPartToClaude(part); len(partJSON) > 0 {
				// Drop empty text parts so a mixed array never emits blocks
				// Anthropic rejects; images and documents always count.
				if !contentPartHasVisibleContent(partJSON) {
					return true
				}
				partsJSON = append(partsJSON, partJSON)
			}
			return true
		})
		if len(partsJSON) > 0 {
			return partsJSON
		}
	}
	text := output.String()
	if output.IsArray() || strings.TrimSpace(text) == "" {
		// An empty standalone output still needs a block so the user message
		// it joins never degenerates to an empty content array. The IsArray
		// guard keeps the raw array dump from leaking in as literal text when
		// every converted part was empty.
		return [][]byte{[]byte(`{"type":"text","text":"Tool result was empty."}`)}
	}
	contentPart := []byte(`{"type":"text","text":""}`)
	contentPart, _ = sjson.SetBytes(contentPart, "text", text)
	return [][]byte{contentPart}
}

// contentPartHasVisibleContent reports whether a converted Claude content
// block carries user-visible payload (non-empty text, image, or document).
func contentPartHasVisibleContent(part []byte) bool {
	switch gjson.GetBytes(part, "type").String() {
	case "text":
		return strings.TrimSpace(gjson.GetBytes(part, "text").String()) != ""
	default:
		// Non-text blocks (image, document, ...) always carry content.
		return true
	}
}
