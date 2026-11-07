package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type imageBoundaryWriter struct {
	gin.ResponseWriter
	writes chan string
}

func (w *imageBoundaryWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	w.writes <- string(b)
	return n, err
}

func TestRawImagesTerminalErrorStartsAtEventBoundary(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	writes := make(chan string, 10)
	c.Writer = &imageBoundaryWriter{ResponseWriter: c.Writer, writes: writes}
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	done := make(chan struct{})
	h := &OpenAIAPIHandler{BaseAPIHandler: &handlers.BaseAPIHandler{Cfg: &sdkconfig.SDKConfig{}}}
	go func() {
		defer close(done)
		h.forwardRawImageStream(context.Background(), c, func(error) {}, data, errs)
	}()
	data <- []byte("event: image_generation.completed\ndata: {\"b64_json\":\"partial")
	<-writes
	errs <- &interfaces.ErrorMessage{StatusCode: 502, Error: errors.New("upstream failed")}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("forwarder did not finish")
	}
	if !strings.Contains(rec.Body.String(), "partial\n\nevent: error\n") {
		t.Fatalf("terminal error was joined to data: %q", rec.Body.String())
	}
}

// Keep-alives are legal between complete SSE events, never inside a data line.
func TestRawImagesKeepAliveDoesNotSplitDataLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil).WithContext(ctx)
	writes := make(chan string, 10)
	c.Writer = &imageBoundaryWriter{ResponseWriter: c.Writer, writes: writes}
	data := make(chan []byte)
	done := make(chan struct{})
	h := &OpenAIAPIHandler{BaseAPIHandler: &handlers.BaseAPIHandler{Cfg: &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 1}}}}
	go func() { defer close(done); h.forwardRawImageStream(ctx, c, func(error) {}, data, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("forwarder did not stop")
		}
	})
	data <- []byte("data: {\"partial\":\"abc")
	<-writes
	select {
	case got := <-writes:
		t.Fatalf("keep-alive split a data line: %q", got)
	case <-time.After(1200 * time.Millisecond):
	}
	data <- []byte("def\"}\n\n")
	<-writes
	select {
	case got := <-writes:
		if got != ": keep-alive\n\n" {
			t.Fatalf("unexpected boundary write: %q", got)
		}
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("no keep-alive at completed event boundary")
	}
}

func TestImagesSSEBoundaryAcrossChunks(t *testing.T) {
	for _, tt := range []struct {
		name    string
		chunks  []string
		count   int
		padding string
	}{
		{"partial", []string{"data: x"}, 0, "\n\n"},
		{"split LF", []string{"data: x\n", "\n"}, 2, ""},
		{"split CRLF", []string{"data: x\r", "\n\r", "\n"}, 2, ""},
		{"one line", []string{"data: x\r\n"}, 1, "\n"},
		{"new event", []string{"data: x\n\n", "data: y"}, 0, "\n\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			count := 2
			for _, chunk := range tt.chunks {
				count = advanceImagesSSENewlineCount(count, []byte(chunk))
			}
			if count != tt.count {
				t.Fatalf("count=%d, want %d", count, tt.count)
			}
			var padding strings.Builder
			ensureImagesSSEEventBoundary(&padding, count)
			if got := padding.String(); got != tt.padding {
				t.Fatalf("padding=%q, want %q", got, tt.padding)
			}
		})
	}
}
