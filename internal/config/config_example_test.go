package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScopedExampleConfigParses(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read %s: %v", path, errRead)
	}
	cfg, errParse := ParseConfigBytes(raw)
	if errParse != nil {
		t.Fatalf("parse %s: %v", path, errParse)
	}
	if cfg == nil {
		t.Fatal("parsed config is nil")
	}
	if cfg.Port != 8317 {
		t.Fatalf("port = %d, want 8317", cfg.Port)
	}
	if cfg.RemoteManagement.DisableControlPanel {
		t.Fatal("scoped example must enable the bundled management panel")
	}
}
