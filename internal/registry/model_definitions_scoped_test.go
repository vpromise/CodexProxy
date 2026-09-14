package registry

import "testing"

func TestGetStaticModelDefinitionsByChannelScopedProviders(t *testing.T) {
	for _, channel := range []string{"claude", "codex"} {
		if models := GetStaticModelDefinitionsByChannel(channel); len(models) == 0 {
			t.Fatalf("channel %q returned no models", channel)
		}
	}
	if models := GetStaticModelDefinitionsByChannel("removed-provider"); models != nil {
		t.Fatalf("removed provider returned models: %#v", models)
	}
}

func TestModelOverrideHeadersFromEmbeddedModels(t *testing.T) {
	const wantUA = "codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)"
	got := ModelOverrideHeaders("gpt-5.6-luna")
	if got == nil || got["user-agent"] != wantUA {
		t.Fatalf("override headers = %#v", got)
	}
	if got := ModelOverrideHeaders("gpt-5.4"); got != nil {
		t.Fatalf("unexpected override headers: %#v", got)
	}
}

func TestWithCodexBuiltinsIncludesImageModels(t *testing.T) {
	models := WithCodexBuiltins(nil)
	found := map[string]bool{}
	for _, model := range models {
		if model != nil {
			found[model.ID] = true
		}
	}
	for _, id := range []string{codexBuiltinImage15ModelID, codexBuiltinImageModelID} {
		if !found[id] {
			t.Fatalf("missing built-in model %q", id)
		}
	}
}
