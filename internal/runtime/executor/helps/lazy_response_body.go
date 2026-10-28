package helps

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/klauspost/compress/zstd"
)

// LazySniffResponseBody postpones compression detection until the first Read.
// The factory retains the caller's zstd memory and window limits.
func LazySniffResponseBody(body io.ReadCloser, newZstd func(io.Reader) (*zstd.Decoder, error)) io.ReadCloser {
	return &lazyResponseBody{body: body, newZstd: newZstd}
}

type lazyResponseBody struct {
	body         io.ReadCloser
	newZstd      func(io.Reader) (*zstd.Decoder, error)
	readMu       sync.Mutex
	initialized  bool
	reader       io.Reader
	closeDecoder func() error
	readErr      error
	closed       atomic.Bool
	closeOnce    sync.Once
	closeErr     error
}

func (b *lazyResponseBody) detect() {
	buffered := bufio.NewReader(b.body)
	b.reader = buffered
	first, _ := buffered.Peek(1)
	if len(first) == 0 || (first[0] != 0x1f && first[0] != 0x28) {
		return
	}
	magic, errPeek := buffered.Peek(4)
	if errPeek != nil && !(errPeek == io.EOF && len(magic) >= 2) {
		return
	}
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		reader, errGzip := gzip.NewReader(buffered)
		if errGzip != nil {
			b.readErr = fmt.Errorf("magic-byte gzip: failed to create reader: %w", errGzip)
			return
		}
		b.reader, b.closeDecoder = reader, reader.Close
	case len(magic) >= 4 && magic[0] == 0x28 && magic[1] == 0xb5 && magic[2] == 0x2f && magic[3] == 0xfd:
		reader, errZstd := b.newZstd(buffered)
		if errZstd != nil {
			b.readErr = fmt.Errorf("magic-byte zstd: failed to create reader: %w", errZstd)
			return
		}
		b.reader, b.closeDecoder = reader, func() error { reader.Close(); return nil }
	}
}

func (b *lazyResponseBody) Read(p []byte) (int, error) {
	b.readMu.Lock()
	defer b.readMu.Unlock()
	if b.closed.Load() {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	if !b.initialized {
		b.initialized = true
		b.detect()
	}
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.reader.Read(p)
}

func (b *lazyResponseBody) Close() error {
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		// Close the transport first: it must unblock a pending Read before waiting
		// for decoder ownership. No decoder can be created after this cleanup.
		b.closeErr = b.body.Close()
		b.readMu.Lock()
		defer b.readMu.Unlock()
		if b.closeDecoder != nil {
			if errDecoder := b.closeDecoder(); b.closeErr == nil {
				b.closeErr = errDecoder
			}
		}
	})
	return b.closeErr
}
