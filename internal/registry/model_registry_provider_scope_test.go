package registry

import "testing"

func TestModelMetadataProviderScope(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("claude-owner", "claude", []*ModelInfo{{ID: "collision", DisplayName: "Claude", Thinking: &ThinkingSupport{Levels: []string{"high"}}}})
	if got := r.GetModelInfo("collision", "codex"); got != nil {
		t.Fatalf("foreign metadata leaked: %+v", got)
	}
	if got := r.GetModelInfo("collision", ""); got == nil || got.DisplayName != "Claude" {
		t.Fatalf("unspecified-provider fallback lost: %+v", got)
	}
	if got := r.GetModelInfo("collision", " CLAUDE "); got == nil || got.DisplayName != "Claude" {
		t.Fatalf("normalized provider missing: %+v", got)
	}
	r.RegisterClient("codex-owner", "codex", []*ModelInfo{{ID: "collision", DisplayName: "Codex", UserDefined: true, Config: &ModelConfig{OverrideHeader: map[string]string{"X-Owner": "codex"}}}})
	got := r.GetModelInfo("collision", "claude")
	if got == nil || got.DisplayName != "Claude" || got.UserDefined || got.Config != nil {
		t.Fatalf("same ID overwrote provider metadata: %+v", got)
	}
	got.Thinking.Levels[0] = "mutated"
	if r.GetModelInfo("collision", "claude").Thinking.Levels[0] != "high" {
		t.Fatal("metadata was not cloned")
	}
	r.UnregisterClient("claude-owner")
	if got := r.GetModelInfo("collision", "claude"); got != nil {
		t.Fatalf("inactive provider fell back to live sibling: %+v", got)
	}
	if got := r.GetModelInfo("collision", "codex"); got == nil || !got.UserDefined {
		t.Fatalf("healthy sibling lost: %+v", got)
	}
}

func TestModelMetadataProviderStaticFallback(t *testing.T) {
	reg := GetGlobalRegistry()
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			models := GetStaticModelDefinitionsByChannel(provider)
			if len(models) == 0 {
				t.Fatal("missing static models")
			}
			id := models[0].ID
			owner := "foreign-static-" + provider
			reg.RegisterClient(owner, "fixture-foreign", []*ModelInfo{{ID: id, UserDefined: true, Config: &ModelConfig{OverrideHeader: map[string]string{"X-Foreign": "unsafe"}}}})
			t.Cleanup(func() { reg.UnregisterClient(owner) })
			expected := LookupStaticModelInfoByChannel(id, provider)
			got := LookupModelInfo(id, " "+provider+" ")
			if expected == nil || got == nil || got.UserDefined || got.DisplayName != expected.DisplayName || got.Config != nil && got.Config.OverrideHeader["X-Foreign"] != "" {
				t.Fatalf("foreign registration shadowed native static metadata: %+v", got)
			}
			got.ID = "mutated"
			if again := LookupStaticModelInfoByChannel(id, provider); again == nil || again.ID != id {
				t.Fatal("static lookup was not cloned")
			}
		})
	}
	if got := LookupStaticModelInfoByChannel("anything", "removed-provider"); got != nil {
		t.Fatalf("removed provider exposed: %+v", got)
	}
}
