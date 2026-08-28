package diff

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type ClaudeModelsSummary struct {
	hash  string
	count int
}

type CodexModelsSummary struct {
	hash  string
	count int
}

func SummarizeClaudeModels(models []config.ClaudeModel) ClaudeModelsSummary {
	if len(models) == 0 {
		return ClaudeModelsSummary{}
	}
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			isCompat := "false"
			if model.IsCompat {
				isCompat = "true"
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|is-compat=" + isCompat + thinkingHashSuffix(model.Thinking))
		}
	})
	return ClaudeModelsSummary{
		hash:  hashJoined(keys),
		count: len(keys),
	}
}

// SummarizeCodexModels hashes Codex model aliases for change detection.
func SummarizeCodexModels(models []config.CodexModel) CodexModelsSummary {
	if len(models) == 0 {
		return CodexModelsSummary{}
	}
	keys := normalizeModelPairs(func(out func(key string)) {
		for _, model := range models {
			name := strings.TrimSpace(model.Name)
			alias := strings.TrimSpace(model.Alias)
			if name == "" && alias == "" {
				continue
			}
			forceMapping := "false"
			if model.ForceMapping {
				forceMapping = "true"
			}
			isCompat := "false"
			if model.IsCompat {
				isCompat = "true"
			}
			out(strings.ToLower(name) + "|" + strings.ToLower(alias) + "|" + strings.TrimSpace(model.DisplayName) + "|force-mapping=" + forceMapping + "|is-compat=" + isCompat + thinkingHashSuffix(model.Thinking))
		}
	})
	return CodexModelsSummary{
		hash:  hashJoined(keys),
		count: len(keys),
	}
}
