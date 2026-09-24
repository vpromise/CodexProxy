package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileStreamingLogWriterPreservesExistingLog(t *testing.T) {
	dir := t.TempDir()
	logger := &FileRequestLogger{enabled: true, logsDir: dir}
	writer, errStart := logger.LogStreamingRequest("/v1/responses", "POST", nil, []byte("request-body"), "a1b2c3d4")
	if errStart != nil {
		t.Fatal(errStart)
	}
	stream := writer.(*FileStreamingLogWriter)
	const original = "previous-request-must-survive"
	if errWrite := os.WriteFile(stream.logFilePath, []byte(original), 0o600); errWrite != nil {
		if errClose := writer.Close(); errClose != nil {
			t.Errorf("close streaming logger: %v", errClose)
		}
		t.Fatal(errWrite)
	}
	writer.WriteChunkAsync([]byte("new-response-body"))
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	previous, errRead := os.ReadFile(stream.logFilePath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if string(previous) != original {
		t.Fatalf("existing request log overwritten: %q", previous)
	}
	files, errList := os.ReadDir(dir)
	if errList != nil {
		t.Fatal(errList)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want two logs and no temporary files", len(files))
	}
	for _, entry := range files {
		path := filepath.Join(dir, entry.Name())
		if path == stream.logFilePath {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			t.Fatal(errInfo)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("new log permissions = %o, want 600", info.Mode().Perm())
		}
		data, errRead := os.ReadFile(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if !strings.HasSuffix(entry.Name(), "-a1b2c3d4.log") || !strings.Contains(string(data), "new-response-body") || !strings.Contains(string(data), "request-body") {
			t.Fatalf("new streaming log missing ID or content: %s: %s", entry.Name(), data)
		}
	}
}
