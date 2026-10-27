package middleware

import (
	"bytes"
	"encoding/hex"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"io"
	"net/http/httptest"
	"testing"
)

func TestRequestLogZstdExpansionBoundPreservesWireBody(t *testing.T) {
	var compressed bytes.Buffer
	encoder, err := zstd.NewWriter(&compressed, zstd.WithWindowSize(1<<20), zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	block := make([]byte, 1<<20)
	for i := 0; i < 40; i++ {
		if _, err = encoder.Write(block); err != nil {
			t.Fatal(err)
		}
	}
	if err = encoder.Close(); err != nil {
		t.Fatal(err)
	}
	raw := compressed.Bytes()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(raw))
	c.Request.Header.Set("Content-Encoding", "zstd")
	info, err := captureRequestInfo(c, true)
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte("[DECOMPRESSED REQUEST BODY TRUNCATED]")
	if !bytes.HasSuffix(info.Body, marker) || int64(len(info.Body)) > maxDeferredErrorRequestBodyBytes+int64(len(marker))+1 {
		t.Fatalf("unexpected log capture length %d", len(info.Body))
	}
	restored, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, raw) {
		t.Fatal("logging modified inference wire bytes")
	}
}

func TestRequestLogZstdOversizedWindowFallsBackToRaw(t *testing.T) {
	// Synthetic frame from upstream ad8bdd3f with a 16 MiB window.
	raw, err := hex.DecodeString("28b52ffd0470b900007b226d6f64656c223a226770742d362e312d736f6c227d2a82afeb")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeCapturedRequestBodyForLogWithLimit(raw, "zstd", 1<<20)
	if !bytes.Equal(got, raw) {
		t.Fatalf("oversized window decoded: %q", got)
	}
}
