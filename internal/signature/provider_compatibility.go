package signature

import "strings"

type SignatureProvider string

const (
	SignatureProviderUnknown SignatureProvider = "unknown"
	SignatureProviderClaude  SignatureProvider = "claude"
	SignatureProviderGPT     SignatureProvider = "gpt"
)

type SignatureBlockKind string

const (
	SignatureBlockKindUnknown        SignatureBlockKind = "unknown"
	SignatureBlockKindClaudeThinking SignatureBlockKind = "claude_thinking"
	SignatureBlockKindGPTReasoning   SignatureBlockKind = "gpt_reasoning"
)

type SignatureCompatibilityAction string

const (
	SignatureActionPreserve                SignatureCompatibilityAction = "preserve"
	SignatureActionDropBlock               SignatureCompatibilityAction = "drop_block"
	SignatureActionNoCompatibleReplacement SignatureCompatibilityAction = "no_compatible_replacement"
)

type SignatureCompatibilityDecision struct {
	TargetProvider      SignatureProvider
	DetectedProvider    SignatureProvider
	BlockKind           SignatureBlockKind
	Compatible          bool
	Action              SignatureCompatibilityAction
	NormalizedSignature string
	Reason              string
}

func SignatureProviderFromModelName(modelName string) SignatureProvider {
	lower := strings.ToLower(strings.TrimSpace(modelName))
	switch {
	case strings.Contains(lower, "claude"):
		return SignatureProviderClaude
	case strings.Contains(lower, "gpt"),
		strings.Contains(lower, "openai"),
		strings.Contains(lower, "codex"),
		strings.HasPrefix(lower, "o1"),
		strings.HasPrefix(lower, "o3"),
		strings.HasPrefix(lower, "o4"):
		return SignatureProviderGPT
	default:
		return SignatureProviderUnknown
	}
}

func DetectSignatureProvider(rawSignature string) SignatureProvider {
	return DetectSignatureProviderForBlock(rawSignature, SignatureBlockKindUnknown)
}

func DetectSignatureProviderForBlock(rawSignature string, _ SignatureBlockKind) SignatureProvider {
	payload := SignaturePayloadWithoutProviderPrefix(rawSignature)
	if payload == "" {
		return SignatureProviderUnknown
	}
	if IsValidClaudeThinkingSignature(payload, ClaudeSignatureValidationOptions{Strict: true}) || IsValidClaudeCAISSignature(payload) {
		return SignatureProviderClaude
	}
	if IsValidGPTReasoningSignature(payload) {
		return SignatureProviderGPT
	}
	return SignatureProviderUnknown
}

func IsSignatureCompatibleWithProvider(targetProvider SignatureProvider, rawSignature string) bool {
	return DecideSignatureCompatibility(targetProvider, rawSignature, SignatureBlockKindUnknown).Compatible
}

func DecideSignatureCompatibility(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) SignatureCompatibilityDecision {
	return DecideSignatureCompatibilityForModel(targetProvider, "", rawSignature, blockKind)
}

func DecideSignatureCompatibilityForModel(targetProvider SignatureProvider, _ string, rawSignature string, blockKind SignatureBlockKind) SignatureCompatibilityDecision {
	if blockKind == "" {
		blockKind = SignatureBlockKindUnknown
	}
	detected := DetectSignatureProviderForBlock(rawSignature, blockKind)
	decision := SignatureCompatibilityDecision{
		TargetProvider:   targetProvider,
		DetectedProvider: detected,
		BlockKind:        blockKind,
	}
	if targetProvider == detected && (targetProvider == SignatureProviderClaude || targetProvider == SignatureProviderGPT) {
		decision.Compatible = true
		decision.Action = SignatureActionPreserve
		decision.NormalizedSignature = normalizeCompatibleSignatureForProvider(targetProvider, rawSignature)
		decision.Reason = "signature provider matches target provider"
		return decision
	}
	decision.Action = SignatureActionDropBlock
	if targetProvider == SignatureProviderUnknown {
		decision.Action = SignatureActionNoCompatibleReplacement
		decision.Reason = "unknown target provider"
	} else {
		decision.Reason = "signature is not compatible with the target provider"
	}
	return decision
}

func SplitSignatureProviderPrefix(rawSignature string) (SignatureProvider, string, bool) {
	prefix, rest, ok := strings.Cut(strings.TrimSpace(rawSignature), "#")
	if !ok {
		return SignatureProviderUnknown, rawSignature, false
	}
	provider := SignatureProviderFromCachePrefix(prefix)
	if provider == SignatureProviderUnknown {
		return SignatureProviderUnknown, rawSignature, false
	}
	return provider, strings.TrimSpace(rest), true
}

func SignatureProviderFromCachePrefix(prefix string) SignatureProvider {
	switch strings.ToLower(strings.TrimSpace(prefix)) {
	case "claude", "anthropic", "cais", "claude-cais", "claude_cais", "ccmax", "claude-code-max", "claude_code_max":
		return SignatureProviderClaude
	case "openai", "gpt", "codex":
		return SignatureProviderGPT
	default:
		return SignatureProviderUnknown
	}
}

func SignaturePayloadWithoutProviderPrefix(rawSignature string) string {
	if _, payload, ok := SplitSignatureProviderPrefix(rawSignature); ok {
		return payload
	}
	return strings.TrimSpace(rawSignature)
}

func CompatibleSignatureForProvider(targetProvider SignatureProvider, rawSignature string) (string, bool) {
	return CompatibleSignatureForProviderBlock(targetProvider, rawSignature, SignatureBlockKindUnknown)
}

func CompatibleSignatureForProviderBlock(targetProvider SignatureProvider, rawSignature string, blockKind SignatureBlockKind) (string, bool) {
	decision := DecideSignatureCompatibility(targetProvider, rawSignature, blockKind)
	if !decision.Compatible || decision.NormalizedSignature == "" {
		return "", false
	}
	return decision.NormalizedSignature, true
}

func normalizeCompatibleSignatureForProvider(targetProvider SignatureProvider, rawSignature string) string {
	payload := SignaturePayloadWithoutProviderPrefix(rawSignature)
	switch targetProvider {
	case SignatureProviderClaude:
		if IsValidClaudeCAISSignature(payload) {
			return payload
		}
		normalized, err := NormalizeClaudeProviderNativeThinkingSignature(payload)
		if err == nil {
			return normalized
		}
	case SignatureProviderGPT:
		if IsValidGPTReasoningSignature(payload) {
			return payload
		}
	}
	return ""
}

func base64AlphabetSet(extra string) [256]bool {
	var set [256]bool
	for char := byte('A'); char <= 'Z'; char++ {
		set[char] = true
	}
	for char := byte('a'); char <= 'z'; char++ {
		set[char] = true
	}
	for char := byte('0'); char <= '9'; char++ {
		set[char] = true
	}
	for index := 0; index < len(extra); index++ {
		set[extra[index]] = true
	}
	return set
}
