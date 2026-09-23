package models

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestCodexClientCatalogCompactEncodingPreservesInstructions(t *testing.T) {
	const instructions = "line 1\nline 2 <tag> & literal"
	payload := BuildResponseForClient([]map[string]any{{"id": "custom-catalog-model", "base_instructions": instructions}}, nil, false, "0.156.0")
	raw, err := MarshalCompact(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("\n")) || bytes.Contains(raw, []byte(`\u003c`)) || !bytes.Contains(raw, []byte("<tag> & literal")) {
		t.Fatal("catalog expanded instruction text or emitted multiple JSON lines")
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	model := decoded["models"].([]any)[0].(map[string]any)
	if model["base_instructions"] != instructions {
		t.Fatal("instruction bytes changed")
	}
	if _, err := MarshalCompact(map[string]any{"invalid": make(chan int)}); err == nil {
		t.Fatal("encoding error was discarded")
	}
}

func TestCodexClientCatalogRequiredNullOptions(t *testing.T) {
	const instructions = "Keep operator instructions <literal> & intact."
	payload := BuildResponseForClient([]map[string]any{{"id": "custom-catalog-model", "base_instructions": instructions}}, nil, false, "0.156.0")
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded.Models) != 1 {
		t.Fatalf("invalid catalog: models=%d error=%v", len(decoded.Models), err)
	}
	for _, key := range []string{"apply_patch_tool_type", "upgrade", "availability_nux"} {
		if got, exists := decoded.Models[0][key]; !exists || string(got) != "null" {
			t.Errorf("required nullable key %s = %s, exists=%v", key, got, exists)
		}
	}
	var gotInstructions string
	if err := json.Unmarshal(decoded.Models[0]["base_instructions"], &gotInstructions); err != nil || gotInstructions != instructions {
		t.Fatalf("custom instructions changed: %q, error=%v", gotInstructions, err)
	}
}
