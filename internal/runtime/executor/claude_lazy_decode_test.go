package executor

import (
	"io"
	"strings"
	"testing"
)

type claudeDecodeProbe struct {
	reads, closes int
	text          *strings.Reader
}

func (b *claudeDecodeProbe) Read(p []byte) (int, error) { b.reads++; return b.text.Read(p) }
func (b *claudeDecodeProbe) Close() error               { b.closes++; return nil }

func TestClaudeDecodeUnlabeledSetupDoesNotRead(t *testing.T) {
	body := &claudeDecodeProbe{text: strings.NewReader("data: ready\n\n")}
	decoded, err := decodeResponseBodyWithMemoryLimit(body, "", claudeMaxDecoderMemory)
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	if body.reads != 0 {
		t.Fatalf("setup read upstream %d times before caller requested bytes", body.reads)
	}
	got, err := io.ReadAll(decoded)
	if err != nil || string(got) != "data: ready\n\n" {
		t.Fatalf("payload = %q, err=%v", got, err)
	}
}

func TestClaudeDecodeCloseBeforeReadClosesOnce(t *testing.T) {
	body := &claudeDecodeProbe{text: strings.NewReader("data: ready\n\n")}
	decoded, err := decodeResponseBodyWithMemoryLimit(body, "", claudeMaxDecoderMemory)
	if err != nil {
		t.Fatal(err)
	}
	if err = decoded.Close(); err != nil {
		t.Fatal(err)
	}
	if err = decoded.Close(); err != nil {
		t.Fatal(err)
	}
	if body.reads != 0 || body.closes != 1 {
		t.Fatalf("reads=%d closes=%d, want 0 and 1", body.reads, body.closes)
	}
}
