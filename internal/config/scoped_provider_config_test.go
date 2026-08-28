package config

import "testing"

func TestScopedProviderConfigDecoding(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`
codex-api-key:
  - api-key: codex-key
    base-url: https://codex.example.com
    weight: 2
    disable-cooling: false
    request-retry: 0
    models:
      - name: codex-upstream
        alias: codex-client
        display-name: Codex Name
        max-context-length: 200000
        is-compat: true
claude-api-key:
  - api-key: claude-key
    base-url: https://claude.example.com
    weight: 3
    disable-cooling: true
    request-retry: 1
    models:
      - name: claude-upstream
        alias: claude-client
        display-name: Claude Name
        max-context-length: 180000
        is-compat: true
openai-compatibility:
  - name: compatible
    base-url: https://compatible.example.com/v1
    disable-cooling: false
    request-retry: 2
    api-key-entries:
      - api-key: compat-key
        weight: 4
    models:
      - name: compat-upstream
        alias: compat-client
        display-name: Compatibility Name
        max-context-length: 160000
        is-compat: true
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.CodexKey) != 1 || len(cfg.ClaudeKey) != 1 || len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("unexpected scoped config: %#v", cfg)
	}
	if got := cfg.CodexKey[0].Models[0]; !got.IsCompat || got.DisplayName != "Codex Name" || got.MaxContextLength != 200000 {
		t.Fatalf("Codex model = %#v", got)
	}
	if got := cfg.ClaudeKey[0].Models[0]; !got.IsCompat || got.DisplayName != "Claude Name" || got.MaxContextLength != 180000 {
		t.Fatalf("Claude model = %#v", got)
	}
	if got := cfg.OpenAICompatibility[0].Models[0]; !got.IsCompat || got.DisplayName != "Compatibility Name" || got.MaxContextLength != 160000 {
		t.Fatalf("compatible model = %#v", got)
	}
	if got := cfg.OpenAICompatibility[0].APIKeyEntries[0].Weight; got == nil || *got != 4 {
		t.Fatalf("compatible weight = %v", got)
	}
}

func TestScopedProviderCredentialWeightValidation(t *testing.T) {
	for _, raw := range []string{
		"codex-api-key:\n  - api-key: key\n    weight: 1000001\n",
		"claude-api-key:\n  - api-key: key\n    weight: 1.5\n",
		"openai-compatibility:\n  - name: p\n    base-url: https://p.example\n    api-key-entries:\n      - api-key: key\n        weight: 1000001\n",
	} {
		if _, errParse := ParseConfigBytes([]byte(raw)); errParse == nil {
			t.Fatalf("invalid weight accepted: %s", raw)
		}
	}
}
