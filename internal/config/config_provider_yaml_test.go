package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveConfigPreservesProviderIdentityDuringReorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	initial := `# Operator note
claude-api-key:
  - api-key: same-fixture
    base-url: https://a.example.com # comment-a
    pool-account-id: 27bfb54e-87d1-473a-8d63-513a875ae215
    cloak:
      mode: auto
      strict-mode: true
      unknown-option: preserve-a
    headers:
      X-Keep: a
      X-Removed: old
  - api-key: same-fixture
    base-url: https://b.example.com # comment-b
    pool-account-id: f79bafcf-7c65-4372-bb59-10ea14aa4d56
    cloak:
      mode: auto
      strict-mode: true
      unknown-option: preserve-b
    headers:
      X-Keep: b
`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ClaudeKey[0].Cloak.StrictMode = false
	delete(cfg.ClaudeKey[0].Headers, "X-Removed")
	cfg.ClaudeKey[0], cfg.ClaudeKey[1] = cfg.ClaudeKey[1], cfg.ClaudeKey[0]
	for range 2 {
		if err := SaveConfigPreserveComments(path, cfg); err != nil {
			t.Fatal(err)
		}
		cfg, err = LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"# Operator note", "https://a.example.com # comment-a", "https://b.example.com # comment-b", "preserve-a", "preserve-b",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("lost associated YAML content %q:\n%s", want, text)
		}
	}
	if len(cfg.ClaudeKey) != 2 || cfg.ClaudeKey[0].BaseURL != "https://b.example.com" ||
		cfg.ClaudeKey[0].PoolAccountID != "f79bafcf-7c65-4372-bb59-10ea14aa4d56" ||
		cfg.ClaudeKey[1].PoolAccountID != "27bfb54e-87d1-473a-8d63-513a875ae215" ||
		!cfg.ClaudeKey[0].Cloak.StrictMode || cfg.ClaudeKey[1].Cloak.StrictMode ||
		len(cfg.ClaudeKey[1].Headers) != 1 || cfg.ClaudeKey[1].Headers["X-Keep"] != "a" {
		t.Fatalf("unexpected reloaded records:\n%s", text)
	}
	if !strings.Contains(strings.SplitN(text, "comment-a", 2)[1], "unknown-option: preserve-a") {
		t.Fatalf("unknown YAML setting moved to the other record:\n%s", text)
	}
}

func TestSaveConfigPreservesExplicitZeroProviderLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	zero := 0
	cfg := &Config{ClaudeKey: []ClaudeKey{{
		APIKey: "fixture", MaxInFlight: &zero, QueueCapacity: &zero, QueueTimeoutSeconds: &zero,
		Cloak: &CloakConfig{},
	}}}
	if err := SaveConfigPreserveComments(path, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	key := loaded.ClaudeKey[0]
	for option, ptr := range map[string]*int{
		"max-in-flight": key.MaxInFlight, "queue-capacity": key.QueueCapacity, "queue-timeout-seconds": key.QueueTimeoutSeconds,
	} {
		if ptr == nil || *ptr != 0 {
			t.Errorf("%s lost its explicit zero", option)
		}
	}
	if key.Cloak == nil {
		t.Error("explicit empty object collapsed to omission")
	}
}
