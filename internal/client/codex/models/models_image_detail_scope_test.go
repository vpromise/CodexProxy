package models

import "testing"

func TestCodexImageDetailOriginalRespectsProviderScope(t *testing.T) {
	for _, tc := range []struct {
		name, model string
		providers   []string
		want        bool
	}{
		{"native", "gpt-6-astra", []string{"codex"}, true},
		{"claude using codex template name", "gpt-6-astra", []string{"claude"}, false},
		{"mixed template", "gpt-6-astra", []string{"codex", "claude"}, false},
		{"unknown template owner", "gpt-6-astra", nil, false},
		{"claude", "claude-opus-5-5", []string{"claude"}, false},
		{"generic fallback", "custom-vision", []string{"openai-compatibility"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := BuildResponseForClient([]map[string]any{{"id": tc.model}}, func(string) []string { return tc.providers }, false, "")
			entries := out["models"].([]map[string]any)
			if len(entries) != 1 {
				t.Fatalf("entries=%v", entries)
			}
			entry := entries[0]
			got, ok := entry["supports_image_detail_original"].(bool)
			if !ok || got != tc.want {
				t.Fatalf("image detail original=%v, want %v", entry["supports_image_detail_original"], tc.want)
			}
			modalities, ok := entry["input_modalities"].([]any)
			if !ok || len(modalities) != 2 || modalities[1] != "image" {
				t.Fatalf("image input lost: %v", modalities)
			}
		})
	}
}
