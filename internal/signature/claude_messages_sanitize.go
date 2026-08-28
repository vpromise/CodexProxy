package signature

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type SignatureSanitizeReport struct {
	TargetProvider     SignatureProvider
	Preserved          int
	DroppedBlocks      int
	DroppedSignatures  int
	ReplacedSignatures int
	Decisions          []SignatureCompatibilityDecision
}

// SanitizeClaudeMessagesForClaudeUpstream removes history that Claude cannot
// safely replay and strips foreign signature metadata from tool calls.
func SanitizeClaudeMessagesForClaudeUpstream(payload []byte, targetModel string, preserveEmptyThinkingBlocks ...bool) ([]byte, SignatureSanitizeReport) {
	report := SignatureSanitizeReport{TargetProvider: SignatureProviderClaude}
	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, report
	}

	preserveEmpty := len(preserveEmptyThinkingBlocks) > 0 && preserveEmptyThinkingBlocks[0]
	keptMessages := make([]string, 0, len(messages.Array()))
	modified := false
	for messageIndex, message := range messages.Array() {
		content := message.Get("content")
		if !content.IsArray() {
			keptMessages = append(keptMessages, message.Raw)
			continue
		}

		keptParts := make([]string, 0, len(content.Array()))
		messageModified := false
		for partIndex, part := range content.Array() {
			switch part.Get("type").String() {
			case "tool_use":
				updated := part.Raw
				changed := false
				for _, path := range []string{"signature", "thoughtSignature", "thought_signature", "extra_content", "model"} {
					if !gjson.Get(updated, path).Exists() {
						continue
					}
					updated, _ = sjson.Delete(updated, path)
					changed = true
				}
				if changed {
					messageModified = true
					report.DroppedSignatures++
				}
				keptParts = append(keptParts, updated)
			case "thinking":
				rawSignature := part.Get("signature").String()
				if preserveEmpty {
					report.Preserved++
					keptParts = append(keptParts, part.Raw)
					continue
				}
				normalized, ok := CompatibleSignatureForProvider(SignatureProviderClaude, rawSignature)
				decision := DecideSignatureCompatibilityForModel(SignatureProviderClaude, targetModel, rawSignature, SignatureBlockKindClaudeThinking)
				decision.Reason = fmt.Sprintf("messages[%d].content[%d]: %s", messageIndex, partIndex, decision.Reason)
				report.Decisions = append(report.Decisions, decision)
				if !ok {
					report.DroppedBlocks++
					messageModified = true
					continue
				}
				report.Preserved++
				if normalized != rawSignature {
					updated, _ := sjson.Set(part.Raw, "signature", normalized)
					keptParts = append(keptParts, updated)
					messageModified = true
					continue
				}
				keptParts = append(keptParts, part.Raw)
			default:
				keptParts = append(keptParts, part.Raw)
			}
		}

		if !messageModified {
			keptMessages = append(keptMessages, message.Raw)
			continue
		}
		modified = true
		if len(keptParts) == 0 {
			continue
		}
		updated, _ := sjson.SetRaw(message.Raw, "content", "["+strings.Join(keptParts, ",")+"]")
		keptMessages = append(keptMessages, updated)
	}

	if !modified {
		return payload, report
	}
	output, _ := sjson.SetRawBytes(payload, "messages", []byte("["+strings.Join(keptMessages, ",")+"]"))
	return output, report
}
