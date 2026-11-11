package helps

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ClaudeBetaFeatures describes the request fields that need Claude Code 2.1.321 betas.
type ClaudeBetaFeatures struct {
	MidConversationTools bool
	PerTurnControl       bool
	Timing               bool
	InlineTools          bool
	ClearAt              bool
	CacheEviction        bool
}

// DetectClaudeBetaFeatures inspects protocol fields, never text or tool schema properties.
func DetectClaudeBetaFeatures(body []byte) ClaudeBetaFeatures {
	root := gjson.ParseBytes(body)
	model := canonicalClaudeBetaModel(root.Get("model").String())
	perTurnControl := claudeBetaModelMatches(model, "claude-opus-5-5") ||
		claudeBetaModelMatches(model, "claude-fable-5-1") ||
		claudeBetaModelMatches(model, "claude-fable-5-2") ||
		claudeBetaModelMatches(model, "claude-sonnet-5-5") || IsClaudeHaiku55Model(model)
	supportsMidConversation := claudeModelSupportsMidConversationTools(model)
	sonnet5 := claudeBetaModelMatches(model, "claude-sonnet-5") && !claudeBetaModelMatches(model, "claude-sonnet-5-5")
	features := ClaudeBetaFeatures{
		MidConversationTools: supportsMidConversation && !sonnet5,
		PerTurnControl:       perTurnControl,
		ClearAt:              ClaudeModelUsesProgressDisplay(model),
	}
	supportsTiming := !IsClaudeHaiku55Model(model) && (perTurnControl ||
		claudeBetaModelMatches(model, "claude-mythos-5-1") || claudeBetaModelMatches(model, "claude-mythos-5-2"))
	features.Timing = supportsTiming && root.Get("output_config.timing").Exists()
	hasEviction := func(block gjson.Result) bool {
		return block.IsObject() && block.Get("cache_control.evict_on_complete").Exists()
	}
	features.CacheEviction = hasEviction(root)
	for _, field := range []string{"system", "tools"} {
		if blocks := root.Get(field); blocks.IsArray() {
			blocks.ForEach(func(_, block gjson.Result) bool {
				features.CacheEviction = features.CacheEviction || hasEviction(block)
				return true
			})
		}
	}
	if messages := root.Get("messages"); messages.IsArray() {
		messages.ForEach(func(_, message gjson.Result) bool {
			features.Timing = features.Timing || (supportsTiming && message.Get("output_config.timing").Exists())
			features.ClearAt = features.ClearAt || (supportsMidConversation && message.Get("clear_at").Exists())
			features.CacheEviction = features.CacheEviction || hasEviction(message)
			if content := message.Get("content"); content.IsArray() {
				content.ForEach(func(_, block gjson.Result) bool {
					features.InlineTools = features.InlineTools || (supportsMidConversation && strings.EqualFold(strings.TrimSpace(block.Get("type").String()), "tool_addition") && block.Get("tool.definition").Exists())
					features.CacheEviction = features.CacheEviction || hasEviction(block)
					return true
				})
			}
			return true
		})
	}
	return features
}

func canonicalClaudeBetaModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	return strings.ReplaceAll(model, ".", "-")
}

func claudeBetaModelMatches(model, version string) bool {
	return model == version || strings.HasPrefix(model, version+"-") || strings.HasPrefix(model, version+"[")
}

// IsClaudeHaiku55Model keeps Haiku 5.5 capability exceptions separate from older Haiku models.
func IsClaudeHaiku55Model(model string) bool {
	return claudeBetaModelMatches(canonicalClaudeBetaModel(model), "claude-haiku-5-5")
}

// ClaudeModelUsesProgressDisplay identifies the models in the captured 2.1.321 CLI profile.
// It selects capability betas without changing the caller's thinking display or history.
func ClaudeModelUsesProgressDisplay(model string) bool {
	model = canonicalClaudeBetaModel(model)
	return claudeBetaModelMatches(model, "claude-opus-5-5") ||
		claudeBetaModelMatches(model, "claude-fable-5-1") ||
		claudeBetaModelMatches(model, "claude-fable-5-2") ||
		claudeBetaModelMatches(model, "claude-sonnet-5") || IsClaudeHaiku55Model(model)
}

// Keep new automatic beta flags within upstream's model capability boundary.
// This does not change the fork's existing system-message placement policy.
func claudeModelSupportsMidConversationTools(model string) bool {
	switch model {
	case "claude-3-5-haiku-20241022", "claude-3-5-haiku-latest",
		"claude-3-7-sonnet-20250219", "claude-3-7-sonnet-latest",
		"claude-haiku-4-5", "claude-haiku-4-5-20251001",
		"claude-opus-4", "claude-opus-4-20250514",
		"claude-opus-4-1", "claude-opus-4-1-20250805",
		"claude-opus-4-5", "claude-opus-4-5-20251101", "claude-opus-4-6", "claude-opus-4-7",
		"claude-sonnet-4", "claude-sonnet-4-20250514",
		"claude-sonnet-4-5", "claude-sonnet-4-5-20250929", "claude-sonnet-4-6":
		return false
	default:
		return true
	}
}
