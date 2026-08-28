package diff

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestBuildConfigChangeDetailsScopedProviders(t *testing.T) {
	oldCfg := &config.Config{
		ClaudeKey:           []config.ClaudeKey{{APIKey: "old", BaseURL: "https://claude.old"}},
		CodexKey:            []config.CodexKey{{APIKey: "old", BaseURL: "https://codex.old"}},
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat", BaseURL: "https://compat.old"}},
	}
	newCfg := &config.Config{
		ClaudeKey:           []config.ClaudeKey{{APIKey: "new", BaseURL: "https://claude.new"}},
		CodexKey:            []config.CodexKey{{APIKey: "new", BaseURL: "https://codex.new"}},
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat", BaseURL: "https://compat.new"}},
	}
	joined := strings.Join(BuildConfigChangeDetails(oldCfg, newCfg), "\n")
	for _, want := range []string{"claude[0].base-url", "codex[0].base-url"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("change details missing %q: %s", want, joined)
		}
	}
}

func TestScopedModelHashesTrackCompatibilityFields(t *testing.T) {
	if ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m"}}) == ComputeClaudeModelsHash([]config.ClaudeModel{{Name: "m", IsCompat: true}}) {
		t.Fatal("Claude model hash ignored IsCompat")
	}
	if ComputeCodexModelsHash([]config.CodexModel{{Name: "m"}}) == ComputeCodexModelsHash([]config.CodexModel{{Name: "m", ForceMapping: true}}) {
		t.Fatal("Codex model hash ignored ForceMapping")
	}
	if ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m"}}) == ComputeOpenAICompatModelsHash([]config.OpenAICompatibilityModel{{Name: "m", IsCompat: true}}) {
		t.Fatal("OpenAI-compatible model hash ignored IsCompat")
	}
}
