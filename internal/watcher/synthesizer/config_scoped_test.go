package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestConfigSynthesizerScopedProviders(t *testing.T) {
	claudeWeight, codexWeight, compatWeight := 2, 3, 4
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{{APIKey: "claude", Weight: &claudeWeight}},
		CodexKey:  []config.CodexKey{{APIKey: "codex", BaseURL: "https://codex.example.com", Weight: &codexWeight}},
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name: "compat", BaseURL: "https://compat.example.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "compat", Weight: &compatWeight}},
		}},
	}
	auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config: cfg, Now: time.Unix(1, 0), IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 3 {
		t.Fatalf("auth count = %d, want 3", len(auths))
	}
	want := map[string]bool{"claude": false, "codex": false, "openai-compatible-compat": false}
	for _, auth := range auths {
		if _, ok := want[auth.Provider]; !ok {
			t.Fatalf("unexpected provider %q", auth.Provider)
		}
		want[auth.Provider] = true
	}
	for provider, found := range want {
		if !found {
			t.Fatalf("provider %q was not synthesized", provider)
		}
	}
}

func TestConfigSynthesizerRejectsInvalidScopedWeight(t *testing.T) {
	invalid := config.MaxCredentialWeight + 1
	_, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config: &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "key", Weight: &invalid}}},
		Now:    time.Unix(1, 0), IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize == nil {
		t.Fatal("Synthesize() accepted invalid weight")
	}
}
