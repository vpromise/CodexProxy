package openai

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type responsesFailingTransport struct {
	*httptest.ResponseRecorder
	writeErr error
	flushErr error
	short    bool
	writes   int
}

func (w *responsesFailingTransport) Write(p []byte) (int, error) {
	w.writes++
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	if w.short {
		return len(p) - 1, nil
	}
	return w.ResponseRecorder.Write(p)
}
func (w *responsesFailingTransport) FlushError() error { return w.flushErr }

func TestResponsesStreamPropagatesTransportErrors(t *testing.T) {
	for _, mode := range []string{"write", "short", "flush"} {
		t.Run(mode, func(t *testing.T) {
			transport := &responsesFailingTransport{ResponseRecorder: httptest.NewRecorder()}
			want := io.ErrClosedPipe
			switch mode {
			case "write":
				transport.writeErr = want
			case "short":
				transport.short = true
				want = io.ErrShortWrite
			case "flush":
				transport.flushErr = want
			}
			c, _ := gin.CreateTestContext(transport)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil))
			data := make(chan []byte, 2)
			data <- []byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
			data <- []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
			close(data)
			var canceled error
			h.forwardResponsesStream(c, c.Writer, func(err error) { canceled = err }, data, nil, nil)
			if !errors.Is(canceled, want) {
				t.Fatalf("cancel=%v; want=%v", canceled, want)
			}
			if transport.writes != 1 {
				t.Fatalf("kept writing after transport failure: writes=%d", transport.writes)
			}
		})
	}
}
