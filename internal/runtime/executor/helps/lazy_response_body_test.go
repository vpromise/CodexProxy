package helps

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

func TestLazySniffResponseBodyDeliversSinglePlainByte(t *testing.T) {
	input, output := io.Pipe()
	body := LazySniffResponseBody(input, func(r io.Reader) (*zstd.Decoder, error) { return zstd.NewReader(r, zstd.WithDecoderConcurrency(1)) })
	defer body.Close()
	defer output.Close()
	result := make(chan error, 1)
	go func() {
		b := make([]byte, 1)
		n, err := body.Read(b)
		if err == nil && (n != 1 || b[0] != 'd') {
			err = errors.New("first byte changed")
		}
		result <- err
	}()
	if _, err := output.Write([]byte("d")); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("plain first byte waited for extra bytes")
	}
}

type lazyCloseProbe struct {
	io.ReadCloser
	closed chan struct{}
	count  atomic.Int32
}

func (p *lazyCloseProbe) Close() error { p.count.Add(1); close(p.closed); return p.ReadCloser.Close() }

func TestLazySniffResponseBodyCloseDuringDecoderCreation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	raw := &lazyCloseProbe{ReadCloser: io.NopCloser(bytes.NewReader([]byte{0x28, 0xb5, 0x2f, 0xfd})), closed: make(chan struct{})}
	expected := errors.New("fixture decoder rejection")
	body := LazySniffResponseBody(raw, func(io.Reader) (*zstd.Decoder, error) { close(entered); <-release; return nil, expected })
	readDone := make(chan error, 1)
	go func() { _, err := body.Read(make([]byte, 1)); readDone <- err }()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- body.Close() }()
	select {
	case <-raw.closed:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("Close waited before closing transport")
	}
	close(release)
	if err := <-readDone; !errors.Is(err, expected) {
		t.Fatalf("Read: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if raw.count.Load() != 1 {
		t.Fatalf("transport closed %d times", raw.count.Load())
	}
	if _, err := body.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("read after close: %v", err)
	}
}

func TestLazySniffResponseBodyCloseUnblocksRead(t *testing.T) {
	input, output := io.Pipe()
	defer output.Close()
	body := LazySniffResponseBody(input, func(r io.Reader) (*zstd.Decoder, error) { return zstd.NewReader(r, zstd.WithDecoderConcurrency(1)) })
	done := make(chan error, 1)
	go func() { _, err := body.Read(make([]byte, 1)); done <- err }()
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("blocked read unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("read did not unblock")
	}
}

func TestLazySniffResponseBodyPreservesCompressedErrors(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = io.WriteString(w, "data: payload\n\n")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		data      []byte
		wantError bool
	}{
		{"gzip", gz.Bytes(), false}, {"broken-gzip", []byte{0x1f, 0x8b, 0, 0}, true}, {"plain", []byte("data: payload\n\n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := LazySniffResponseBody(io.NopCloser(bytes.NewReader(tc.data)), func(r io.Reader) (*zstd.Decoder, error) { return zstd.NewReader(r, zstd.WithDecoderConcurrency(1)) })
			defer body.Close()
			data, err := io.ReadAll(body)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "magic-byte gzip") {
					t.Fatalf("error=%v", err)
				}
			} else if err != nil || string(data) != "data: payload\n\n" {
				t.Fatalf("payload=%q err=%v", data, err)
			}
		})
	}
}
