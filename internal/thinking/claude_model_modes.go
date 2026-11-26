package thinking

import (
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
)

// ValidateNativeClaudeModelRequest checks explicit Messages API options before
// cross-format compatibility helpers can convert or remove them.
func ValidateNativeClaudeModelRequest(body []byte, modelInfo *registry.ModelInfo, apiVersion string, stream, countTokens bool) error {
	if modelInfo == nil || modelInfo.UserDefined || modelInfo.IsCompat {
		return nil
	}
	if err := ValidateClaudeModelRequest(body, modelInfo); err != nil {
		return err
	}
	invalid := func(message string) error {
		return NewThinkingErrorWithModel(ErrInvalidThinkingConfig, message, modelInfo.ID)
	}
	toolChoice := gjson.GetBytes(body, "tool_choice.type").String()
	forcedTool := toolChoice == "any" || toolChoice == "tool"
	model := claudeThinkingModel(modelInfo)
	if forcedTool {
		switch model {
		case "claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1", "claude-fable-5-2", "claude-mythos-5-1", "claude-mythos-5-2":
			return invalid("tool_choice type tool and any are not supported for this model")
		}
	}
	if maxTokens := gjson.GetBytes(body, "max_tokens"); !countTokens && maxTokens.Type == gjson.Number && maxTokens.Num == 0 {
		if stream || gjson.GetBytes(body, "stream").Bool() || gjson.GetBytes(body, "thinking.type").String() == "enabled" ||
			gjson.GetBytes(body, "output_config.format").IsObject() || forcedTool {
			return invalid("max_tokens: 0 requires a non-streaming request without manual thinking, output format, or forced tool choice")
		}
	}

	temperature := gjson.GetBytes(body, "temperature")
	topP := gjson.GetBytes(body, "top_p")
	topK := gjson.GetBytes(body, "top_k")
	if !temperature.Exists() && !topP.Exists() && !topK.Exists() {
		return nil
	}
	if _, err := time.Parse(time.DateOnly, apiVersion); err == nil && apiVersion >= "2026-10-21" {
		return invalid("temperature, top_p and top_k are not supported by this Anthropic API version")
	}
	switch model {
	case "claude-opus-4-7", "claude-opus-4-8", "claude-opus-5", "claude-opus-5-5",
		"claude-sonnet-5", "claude-sonnet-5-5", "claude-haiku-5-5",
		"claude-fable-5", "claude-fable-5-1", "claude-fable-5-2",
		"claude-mythos-5", "claude-mythos-5-1", "claude-mythos-5-2", "claude-mythos-preview":
		if temperature.Type != gjson.Null && (temperature.Type != gjson.Number || temperature.Num != 1) {
			return invalid("non-default temperature is not supported for this model")
		}
		if topP.Type != gjson.Null && (topP.Type != gjson.Number || topP.Num != 0.99) {
			return invalid("non-default top_p is not supported for this model")
		}
		if topK.Type != gjson.Null {
			return invalid("top_k is deprecated for this model")
		}
		if temperature.Type != gjson.Null && topP.Type != gjson.Null {
			return invalid("temperature and top_p cannot both be set for this model")
		}
	}
	return nil
}

// ClaudeModelChecksThinkingPrefix reports whether Anthropic can bind a
// thinking block to the original system, tools, and conversation history.
func ClaudeModelChecksThinkingPrefix(modelInfo *registry.ModelInfo) bool {
	switch claudeThinkingModel(modelInfo) {
	case "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5",
		"claude-fable-5-1", "claude-fable-5-2", "claude-mythos-5-1", "claude-mythos-5-2":
		return true
	default:
		return false
	}
}

// ClaudeThinkingTypeForNone maps canonical none to the model's supported wire
// setting. Always-on models must report an error instead of enabling thinking.
// https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting
func ClaudeThinkingTypeForNone(modelInfo *registry.ModelInfo) (string, error) {
	switch claudeThinkingModel(modelInfo) {
	case "claude-sonnet-5-5":
		return "between_tools", nil
	case "claude-opus-5-5", "claude-fable-5", "claude-fable-5-1", "claude-fable-5-2",
		"claude-mythos-5", "claude-mythos-5-1", "claude-mythos-5-2", "claude-mythos-preview":
		return "", NewThinkingErrorWithModel(ErrInvalidThinkingConfig, "thinking cannot be disabled for this model; use adaptive thinking with a supported effort", modelInfo.ID)
	default:
		return "disabled", nil
	}
}

// ClaudeThinkingNoneSupportsEffort reports whether the lowest thinking mode
// still accepts a low, medium, or high effort value independently of activation.
func ClaudeThinkingNoneSupportsEffort(modelInfo *registry.ModelInfo) bool {
	switch claudeThinkingModel(modelInfo) {
	case "claude-sonnet-5-5", "claude-haiku-5-5", "claude-opus-5":
		return true
	default:
		return false
	}
}

// ValidateClaudeModelRequest checks the final Claude body without rewriting it.
// The caller must supply the model definition selected for the final model ID,
// retaining UserDefined for explicit compatible-model overrides.
func ValidateClaudeModelRequest(body []byte, modelInfo *registry.ModelInfo) error {
	if errMode := validateClaudeThinkingMode(body, modelInfo); errMode != nil {
		return errMode
	}
	model := claudeThinkingModel(modelInfo)
	mode := gjson.GetBytes(body, "thinking.type").String()
	if model == "claude-sonnet-5-5" && mode == "disabled" {
		return NewThinkingErrorWithModel(ErrInvalidThinkingConfig, "Claude Sonnet 5.5 requires thinking.type between_tools to turn off up-front thinking", modelInfo.ID)
	}
	if mode != "enabled" {
		return nil
	}
	switch model {
	case "claude-opus-5", "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5",
		"claude-fable-5", "claude-fable-5-1", "claude-fable-5-2",
		"claude-mythos-5", "claude-mythos-5-1", "claude-mythos-5-2":
		return NewThinkingErrorWithModel(ErrInvalidThinkingConfig, "manual thinking budgets are not supported for this model; use adaptive thinking with a supported effort", modelInfo.ID)
	default:
		return nil
	}
}

func claudeThinkingModel(modelInfo *registry.ModelInfo) string {
	if IsUserDefinedModel(modelInfo) || modelInfo.IsCompat || (modelInfo.Type != "" && !strings.EqualFold(modelInfo.Type, "claude")) {
		return ""
	}
	model := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(modelInfo.ID)), ".", "-")
	// Registry snapshots may retain a dated model ID. Match only its eight-digit
	// date suffix so future version numbers cannot match an older model family.
	if separator := strings.LastIndexByte(model, '-'); separator >= 0 {
		suffix := model[separator+1:]
		if len(suffix) == 8 && strings.Trim(suffix, "0123456789") == "" {
			model = model[:separator]
		}
	}
	switch model {
	case "claude-sonnet-5-5", "claude-haiku-5-5", "claude-opus-5", "claude-opus-5-5",
		"claude-fable-5", "claude-fable-5-1", "claude-fable-5-2",
		"claude-mythos-5", "claude-mythos-5-1", "claude-mythos-5-2", "claude-mythos-preview",
		"claude-sonnet-5", "claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6",
		"claude-sonnet-4-6", "claude-opus-4-5", "claude-sonnet-4-5", "claude-haiku-4-5":
		return model
	default:
		return ""
	}
}

// validateClaudeThinkingMode checks the extra constraints on models whose
// disabled mode retains an effort setting or whose lowest mode is between_tools.
// Unknown and explicitly configured model contracts remain upstream-owned.
func validateClaudeThinkingMode(body []byte, modelInfo *registry.ModelInfo) error {
	model := claudeThinkingModel(modelInfo)
	if model == "" {
		return nil
	}
	mode := gjson.GetBytes(body, "thinking.type").String()
	if mode != "disabled" && mode != "between_tools" {
		return nil
	}
	invalid := func(message string) error {
		return NewThinkingErrorWithModel(ErrInvalidThinkingConfig, message, modelInfo.ID)
	}
	if mode == "between_tools" {
		if model != "claude-sonnet-5-5" {
			return invalid("thinking.type between_tools is supported only by Claude Sonnet 5.5")
		}
		if len(gjson.GetBytes(body, "thinking").Map()) != 1 {
			return invalid("thinking.type between_tools accepts no display, budget, or other thinking fields")
		}
	}
	if _, errMode := ClaudeThinkingTypeForNone(modelInfo); errMode != nil {
		return errMode
	}
	if model != "claude-sonnet-5-5" && model != "claude-haiku-5-5" && model != "claude-opus-5" {
		return nil
	}
	if model == "claude-haiku-5-5" && gjson.GetBytes(body, "thinking.block_binding").Exists() {
		return invalid("thinking.block_binding requires adaptive thinking on Claude Haiku 5.5")
	}
	effort := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "output_config.effort").String()))
	if effort == "xhigh" || effort == "max" {
		return invalid(fmt.Sprintf("thinking.type %s requires effort high or below for this model", mode))
	}
	if model == "claude-opus-5" {
		return nil
	}
	// Native defaults are high on Sonnet 5.5 and medium on Haiku 5.5. These
	// modes reject per-message effort changes even when both levels are valid.
	if effort == "" {
		effort = "high"
		if model == "claude-haiku-5-5" {
			effort = "medium"
		}
	}
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		if turnEffort := message.Get("output_config.effort"); turnEffort.Exists() && strings.ToLower(strings.TrimSpace(turnEffort.String())) != effort {
			return invalid(fmt.Sprintf("thinking.type %s does not allow per-message effort changes for this model", mode))
		}
	}
	return nil
}
