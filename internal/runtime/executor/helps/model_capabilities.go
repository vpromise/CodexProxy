package helps

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

// APIKeyModelIsCompat reports whether the selected API-key model enables
// compatibility handling for Claude thinking blocks.
func APIKeyModelIsCompat(req cliproxyexecutor.Request) bool {
	modelInfo, ok := cliproxyauth.ResolvedAPIKeyModelInfo(req)
	return ok && modelInfo != nil && modelInfo.IsCompat
}

// ApplyRequestThinking preserves the registry lookup path unless the auth
// manager bound an exact configured API-key model definition to this attempt.
func ApplyRequestThinking(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, fromFormat, toFormat, provider string) ([]byte, error) {
	originalSource := opts.OriginalRequest
	if len(originalSource) == 0 {
		originalSource = req.Payload
	}
	summaryConfig := translatedRequestSummaryConfig(body, req.Payload, originalSource, req.Model, fromFormat, toFormat)
	if modelInfo, ok := cliproxyauth.ResolvedAPIKeyModelInfo(req); ok {
		return thinking.ApplyThinkingWithModelInfoAndSummary(body, originalSource, req.Model, fromFormat, toFormat, provider, modelInfo, summaryConfig)
	}
	return thinking.ApplyThinkingWithSummary(body, req.Model, fromFormat, toFormat, provider, summaryConfig)
}

// ValidateFinalClaudeThinking validates the finished body without applying
// thinking again. A late payload model override must not inherit a different
// configured model's compatibility contract.
func ValidateFinalClaudeThinking(body []byte, req cliproxyexecutor.Request) error {
	finalModel := strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(gjson.GetBytes(body, "model").String())).ModelName)
	var modelInfo *registry.ModelInfo
	if selectedInfo, ok := cliproxyauth.ResolvedAPIKeyModelInfo(req); ok {
		selectedModel := strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(selectedInfo.ID)).ModelName)
		if selectedModel == "" {
			selectedModel = strings.TrimSpace(thinking.ParseSuffix(strings.TrimSpace(req.Model)).ModelName)
		}
		if selectedModel == finalModel {
			selectedCopy := *selectedInfo
			selectedCopy.ID = finalModel
			modelInfo = &selectedCopy
		}
	}
	if modelInfo == nil {
		modelInfo = registry.LookupModelInfo(finalModel, "claude")
	}
	return thinking.ValidateClaudeModelRequest(body, modelInfo)
}
