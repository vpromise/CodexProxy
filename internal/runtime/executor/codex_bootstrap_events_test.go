package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	codexKeepaliveEvent   = `{"type":"keepalive","sequence_number":1}`
	codexOutputDeltaEvent = `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`
)

func codexWebsocketRawServer(t *testing.T, frames []string, tail ...string) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			return
		}
		for _, frame := range frames {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
				return
			}
		}
		for _, frame := range tail {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(frame))
		}
	}))
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_FrameBudgetReleasesStream(t *testing.T) {
	frames := make([]string, 0, helps.CodexBootstrapMaxBufferedFrames+1)
	for i := 0; i < helps.CodexBootstrapMaxBufferedFrames+1; i++ {
		frames = append(frames, codexInProgressEvent)
	}
	server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("the frame budget must release the stream before the overload arrives: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the frame budget released the stream")
	}
	drainChunks(result)
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_BudgetBoundaryIsExact(t *testing.T) {
	send := func(n int) error {
		frames := make([]string, n)
		for i := range frames {
			frames[i] = codexInProgressEvent
		}
		server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
		defer server.Close()
		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if result != nil {
			drainChunks(result)
		}
		return err
	}
	if err := send(helps.CodexBootstrapMaxBufferedFrames - 1); err == nil {
		t.Fatalf("%d messages are inside the budget and must still fail over", helps.CodexBootstrapMaxBufferedFrames-1)
	}
	// The last message the window still admits. Without this case a budget one frame too small
	// looks identical, since the overload is recognised before the window is consulted and so the
	// max-1 case fails over either way.
	if err := send(helps.CodexBootstrapMaxBufferedFrames); err == nil {
		t.Fatalf("%d messages exactly fill the budget and must still fail over", helps.CodexBootstrapMaxBufferedFrames)
	}
	if err := send(helps.CodexBootstrapMaxBufferedFrames + 1); err != nil {
		t.Fatalf("%d messages exceed the budget and must release: %v", helps.CodexBootstrapMaxBufferedFrames+1, err)
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_ByteCapOrderingAndSeed(t *testing.T) {
	t.Run("oversized message is not admitted", func(t *testing.T) {
		oversized := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("q", helps.CodexBootstrapMaxBufferedBytes*2) + `"}}`
		server := codexWebsocketRawServer(t, []string{oversized}, codexOverloadEvent)
		defer server.Close()

		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil {
			t.Fatalf("a message larger than the cap must be released, not admitted: %v", err)
		}
		if result == nil {
			t.Fatal("expected a stream result")
		}
		drainChunks(result)
	})

	t.Run("budget counts the upstream message", func(t *testing.T) {
		frame := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("w", helps.CodexBootstrapMaxBufferedBytes/8) + `"}}`
		frames := make([]string, 10)
		for i := range frames {
			frames[i] = frame
		}
		server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
		defer server.Close()

		req, _ := codexWebsocketRequest()
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil {
			t.Fatalf("ten messages of cap/8 must exhaust the byte budget: %v", err)
		}
		if result == nil {
			t.Fatal("expected a stream result")
		}
		drainChunks(result)
	})
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_BudgetFrameIsNotDropped(t *testing.T) {
	frames := make([]string, 0, helps.CodexBootstrapMaxBufferedFrames+2)
	for i := 0; i < helps.CodexBootstrapMaxBufferedFrames; i++ {
		frames = append(frames, codexInProgressEvent)
	}
	frames = append(frames, `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"FIRSTTOKEN"}`)
	server := codexWebsocketRawServer(t, frames, codexCompletedEventBody)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("unexpected ExecuteStream error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result")
	}
	combined, _ := drainChunks(result)
	if !strings.Contains(combined, "FIRSTTOKEN") {
		t.Fatalf("the frame that tripped the budget was dropped: %s", combined)
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_SkippedFramesExhaustTheWindow(t *testing.T) {
	cases := []struct {
		name  string
		frame string
	}{
		{"whitespace only frames", "   \n\t "},
		{"empty frames", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := make([]string, helps.CodexBootstrapMaxBufferedFrames+2)
			for i := range frames {
				frames[i] = tc.frame
			}
			server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
			defer server.Close()

			req, opts := codexWebsocketRequest()
			result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

			if err != nil {
				t.Fatalf("the window must release before the overload arrives, got failover: %v", err)
			}
			if result == nil {
				t.Fatal("expected a stream result once the window was exhausted")
			}
			drainChunks(result)
		})
	}
}

func TestCodexExecutor_BootstrapBuffering_OversizedFrameIsNotAdmitted(t *testing.T) {
	oversized := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("q", helps.CodexBootstrapMaxBufferedBytes*2) + `"}}`
	body := "data: " + oversized + "\n" + "data: " + codexOverloadEvent + "\n\n"
	server := codexSSERawServer(body)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("a frame larger than the cap must be released, not admitted: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the oversized frame released the stream")
	}
	drainChunks(result)
}

func TestCodexExecutor_BootstrapBuffering_ByteCapCountsUpstreamFrames(t *testing.T) {
	frame := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("w", helps.CodexBootstrapMaxBufferedBytes/8) + `"}}`
	body := strings.Repeat("data: "+frame+"\n\n", 10) + "data: " + codexOverloadEvent + "\n\n"
	server := codexSSERawServer(body)
	defer server.Close()

	req, _ := codexTestRequest()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("ten frames of cap/8 must exhaust the byte budget: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte budget released the stream")
	}
	drainChunks(result)
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_ByteCapReleasesStream(t *testing.T) {
	third := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("z", helps.CodexBootstrapMaxBufferedBytes/3) + `"}}`
	server := codexWebsocketRawServer(t, []string{third, third, third, third}, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err != nil {
		t.Fatalf("the websocket byte cap must release the stream before the overload arrives: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte cap released the stream")
	}
	drainChunks(result)
}

func codexSSERawServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
}

func TestCodexExecutor_BootstrapBuffering_BudgetBoundaryIsExact(t *testing.T) {
	overload := "data: " + codexOverloadEvent + "\n\n"

	t.Run("one line under the budget still fails over", func(t *testing.T) {
		body := strings.Repeat(": keepalive\n", helps.CodexBootstrapMaxBufferedFrames-1) + overload
		server := codexSSERawServer(body)
		defer server.Close()

		req, opts := codexTestRequest()
		_, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil {
			t.Fatalf("%d held lines are inside the %d-line budget and must still fail over", helps.CodexBootstrapMaxBufferedFrames-1, helps.CodexBootstrapMaxBufferedFrames)
		}
	})

	t.Run("exactly the budget still fails over", func(t *testing.T) {
		body := strings.Repeat(": keepalive\n", helps.CodexBootstrapMaxBufferedFrames) + overload
		server := codexSSERawServer(body)
		defer server.Close()

		req, opts := codexTestRequest()
		_, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil {
			t.Fatalf("%d held lines fill the budget exactly and must still fail over", helps.CodexBootstrapMaxBufferedFrames)
		}
	})

	t.Run("one line over the budget releases", func(t *testing.T) {
		body := strings.Repeat(": keepalive\n", helps.CodexBootstrapMaxBufferedFrames+1) + overload
		server := codexSSERawServer(body)
		defer server.Close()

		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil {
			t.Fatalf("%d held lines exceed the %d-line budget and must release: %v", helps.CodexBootstrapMaxBufferedFrames+1, helps.CodexBootstrapMaxBufferedFrames, err)
		}
		if result == nil {
			t.Fatal("expected a stream result once the budget released the stream")
		}
		drainChunks(result)
	})
}

func TestCodexExecutor_BootstrapBuffering_EmptyDataFrameDoesNotReleaseStream(t *testing.T) {
	for _, frame := range []string{"data:\n\n", "data: \n\n", "data:   \t\n\n"} {
		body := frame + "data: " + codexOverloadEvent + "\n\n"
		server := codexSSERawServer(body)

		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil {
			t.Errorf("an empty data frame %q must keep the bootstrap window open", frame)
		}
		if result != nil {
			t.Errorf("frame %q: expected nil result so no buffered chunk can reach the client", frame)
		}
		server.Close()
	}
}

func TestCodexExecutor_BootstrapBuffering_BoundHoldsForEverySSEFraming(t *testing.T) {
	overload := "data: " + codexOverloadEvent + "\n\n"
	handshake := `{"type":"response.in_progress","response":{"id":"resp_1"}}`

	cases := []struct {
		name string
		body string
	}{
		{
			// Comment heartbeats need no blank separator at all.
			name: "comment heartbeats without blank separators",
			body: strings.Repeat(": keepalive\n", helps.CodexBootstrapMaxBufferedFrames*4) + overload,
		},
		{
			// A data: line is a complete frame; a blank line is optional between them.
			name: "data frames without blank separators",
			body: strings.Repeat("data: "+handshake+"\n", helps.CodexBootstrapMaxBufferedFrames*4) + overload,
		},
		{
			// event:/data:/blank, the framing the canonical test server emits.
			name: "three line framing",
			body: strings.Repeat("event: response.in_progress\ndata: "+handshake+"\n\n", helps.CodexBootstrapMaxBufferedFrames*4) + overload,
		},
		{
			// Extra blank separators are legal and must not be mistaken for extra frames.
			name: "double blank separators",
			body: strings.Repeat("data: "+handshake+"\n\n\n", helps.CodexBootstrapMaxBufferedFrames*4) + overload,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSERawServer(tc.body)
			defer server.Close()

			req, _ := codexTestRequest()
			// Chat Completions renders an unrecognised frame as zero chunks, so a bound derived from
			// the downstream chunk count would never advance here.
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

			if err != nil {
				t.Fatalf("the bound must release the stream before the overload arrives, got failover: %v", err)
			}
			if result == nil {
				t.Fatal("expected a stream result once the bound released the stream")
			}
			if _, streamErr := drainChunks(result); streamErr == nil {
				t.Fatal("expected the overload to arrive in-stream after the bound released the stream")
			}
		})
	}
}

func TestCodexExecutor_BootstrapBuffering_ByteCapReleasesStream(t *testing.T) {
	// Frames that are individually well inside the cap but add up past it must still release, which
	// is what makes this a cap on the buffer rather than a per-frame size limit.
	third := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("y", helps.CodexBootstrapMaxBufferedBytes/3) + `"}}`
	body := "data: " + third + "\n\n" + "data: " + third + "\n\n" + "data: " + third + "\n\n" +
		"data: " + third + "\n\n" + "data: " + codexOverloadEvent + "\n\n"
	server := codexSSERawServer(body)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err != nil {
		t.Fatalf("the byte cap must release the stream before the overload arrives, got failover: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte cap released the stream")
	}
}

func TestCodexExecutor_BootstrapBuffering_NonContentFramesDoNotReleaseStream(t *testing.T) {
	cases := []struct{ name, frame string }{
		{"keepalive", codexKeepaliveEvent},
		{"output item added", codexOutputAddedEvent},
		{"content part added", `{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`},
		{"reasoning summary part added", `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, tc.frame, codexOverloadEvent)
			defer server.Close()

			req, opts := codexTestRequest()
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

			if err == nil {
				t.Fatalf("a %s frame must keep the bootstrap window open", tc.name)
			}
			if result != nil {
				t.Fatal("expected nil result so no buffered chunk can reach the client")
			}
			if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
				t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_NonContentFramesDoNotReleaseStream(t *testing.T) {
	for _, frame := range []string{codexKeepaliveEvent, codexOutputAddedEvent} {
		server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, frame, codexOverloadEvent)
		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil || result != nil {
			t.Fatalf("frame %s must keep the websocket bootstrap window open", frame)
		}
		server.Close()
	}
}

func TestCodexExecutor_BootstrapBuffering_ContentFrameReleasesBeforeOverload(t *testing.T) {
	for _, tc := range codexReleasingFrameCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, tc.frame, codexOverloadEvent)
			defer server.Close()

			req, opts := codexTestRequest()
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
			if err != nil {
				t.Fatalf("a %s frame must release the stream, not fail the attempt over: %v", tc.name, err)
			}
			if result == nil {
				t.Fatalf("a %s frame must release the stream", tc.name)
			}
			if _, streamErr := drainChunks(result); streamErr == nil {
				t.Fatalf("the overload after a %s frame must be delivered in-stream", tc.name)
			}
		})
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_ContentFrameReleasesBeforeOverload(t *testing.T) {
	for _, tc := range codexReleasingFrameCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, tc.frame, codexOverloadEvent)
			defer server.Close()

			req, opts := codexWebsocketRequest()
			result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
			if err != nil {
				t.Fatalf("a %s frame must release the stream, not fail the attempt over: %v", tc.name, err)
			}
			if result == nil {
				t.Fatalf("a %s frame must release the stream", tc.name)
			}
			if _, streamErr := drainChunks(result); streamErr == nil {
				t.Fatalf("the overload after a %s frame must be delivered in-stream", tc.name)
			}
		})
	}
}

func codexReleasingFrameCases() []struct{ name, frame string } {
	return []struct{ name, frame string }{
		{"output text delta", codexOutputDeltaEvent},
		{"web search call announced", `{"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call","status":"in_progress"},"output_index":0}`},
	}
}

func TestCodexExecutor_BootstrapBuffering_CancelDuringBootstrapIsNotAnUpstreamFailure(t *testing.T) {
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + codexCreatedEvent + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-released
	}))
	defer server.Close()
	defer close(released)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(ctx, codexTestAuth(server.URL), req, opts)
	if result != nil {
		drainChunks(result)
	}
	// Identity rather than errors.Is, so a transport error that merely wraps the cancellation cannot
	// satisfy it.
	if err != context.Canceled {
		t.Fatalf("a cancelled caller must surface as context.Canceled itself, got %T: %v", err, err)
	}
}

func TestCodexExecutor_BootstrapBuffering_HeartbeatArithmeticPerFraming(t *testing.T) {
	overload := "event: error\ndata: " + codexOverloadEvent + "\n\n"
	cases := []struct {
		name      string
		heartbeat string
		protected int
	}{
		{"three-line event:/data:/blank", "event: keepalive\ndata: " + codexKeepaliveEvent + "\n\n", 15},
		{"two-line : keepalive comment", ": keepalive\n\n", 23},
		{"one-line : keepalive comment", ": keepalive\n", 47},
	}
	failsOver := func(t *testing.T, heartbeats int, heartbeat, overload string) bool {
		t.Helper()
		server := codexSSERawServer(strings.Repeat(heartbeat, heartbeats) + overload)
		defer server.Close()
		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if result != nil {
			drainChunks(result)
		}
		return err != nil
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !failsOver(t, tc.protected, tc.heartbeat, overload) {
				t.Fatalf("%d heartbeats of the %s framing must still fail over; config.example.yaml documents %d", tc.protected, tc.name, tc.protected)
			}
			if failsOver(t, tc.protected+1, tc.heartbeat, overload) {
				t.Fatalf("%d heartbeats of the %s framing exceed the budget and must release the stream", tc.protected+1, tc.name)
			}
		})
	}
}

func TestCodexExecutor_BootstrapBuffering_ByteCapCountsTranslatedChunks(t *testing.T) {
	padded := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("p", 300<<10) + `"}}`
	server := codexSSEServer(padded, padded, codexOverloadEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("two frames whose upstream bytes fit the cap but whose chunks do not must release the stream: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte budget released the stream")
	}
	if _, streamErr := drainChunks(result); streamErr == nil {
		t.Fatal("expected the overload to arrive in-stream after the byte budget released")
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_ByteCapCountsTranslatedChunks(t *testing.T) {
	padded := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("p", 300<<10) + `"}}`
	server := codexWebsocketRawServer(t, []string{padded, padded}, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("two frames whose upstream bytes fit the cap but whose chunks do not must release the stream: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte budget released the stream")
	}
	if _, streamErr := drainChunks(result); streamErr == nil {
		t.Fatal("expected the overload to arrive in-stream after the byte budget released")
	}
}

func TestIsCodexBootstrapBufferableEvent(t *testing.T) {
	hold := []string{
		`{"type":"response.created"}`,
		`{"type":"response.in_progress"}`,
		`{"type":"codex.rate_limits"}`,
		`{"type":"codex.response.metadata"}`,
		codexKeepaliveEvent,
		codexOutputAddedEvent,
		`{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning"}}`,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","arguments":""}}`,
		`{"type":"response.output_item.added","item":{"id":"ct_1","type":"custom_tool_call","input":""}}`,
		`{"type":"response.output_item.added","item":{"id":"msg_2","type":"message","content":[{"type":"output_text","text":""}]}}`,
		`{"type":"response.output_item.added","item":{"id":"msg_3","type":"message","content":[{"type":"refusal","refusal":""}]}}`,
		`{"type":"response.output_item.added","item":{"id":"rs_2","type":"reasoning","summary":[{"type":"summary_text","text":""}]}}`,
		`{"type":"response.output_item.added","item":{"id":"rs_3","type":"reasoning","content":[{"type":"reasoning_text","text":""}]}}`,
		`{"type":"response.content_part.added","part":{"type":"text","text":""}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`,
		``,
		`   `,
	}
	for _, payload := range hold {
		eventType := gjson.GetBytes([]byte(payload), "type").String()
		if !helps.IsCodexBootstrapBufferableEvent(eventType, []byte(payload)) {
			t.Errorf("must stay bufferable: %s", payload)
		}
	}

	release := []string{
		`{"type":"response.output_text.delta","delta":"hi"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"x"}`,
		`{"type":"response.shell_call_command.delta","delta":"ls"}`,
		`{"type":"response.shell_call_output_content.delta","delta":{"stdout":"x"}}`,
		`{"type":"response.shell_call_output_content.done","output":[]}`,
		`{"type":"response.output_item.done","item":{"id":"m1","type":"message"}}`,
		`{"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
		`{"type":"response.output_item.added","item":{"id":"fs_1","type":"file_search_call"}}`,
		`{"type":"response.output_item.added","item":{"id":"ig_1","type":"image_generation_call"}}`,
		`{"type":"response.output_item.added","item":{"id":"x_1","type":"some_future_server_tool"}}`,
		`{"type":"response.content_part.added","part":{"type":"output_text","text":"already here"}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":"already here"}}`,
		`{"type":"response.content_part.added","part":{"type":"output_audio","audio":"AAAA"}}`,
		`{"type":"response.content_part.added","part":{"type":"refusal","refusal":"I cannot help"}}`,
		`{"type":"response.output_item.added","item":{"id":"rf1","type":"message","content":[{"type":"refusal","refusal":"I cannot help"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"m1","type":"message","content":[{"type":"output_text","text":"already generated"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"r1","type":"reasoning","summary":[{"type":"summary_text","text":"already reasoned"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"f1","type":"function_call","arguments":"{\"path\":\"/\"}"}}`,
		`{"type":"response.output_item.added","item":{"id":"c1","type":"custom_tool_call","input":"already here"}}`,
		`{"type":"response.output_item.added","item":{"id":"a1","type":"message","content":[{"type":"output_audio","audio":"AAAA"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"i1","type":"message","content":[{"type":"output_image","image_url":"data:x"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"r2","type":"reasoning","encrypted_content":"BLOB"}}`,
		`{"type":"response.output_item.added","item":{"id":"r3","type":"reasoning","content":[{"type":"reasoning_text","text":"already reasoned"}]}}`,
		`{"type":"response.web_search_call.searching","item_id":"ws_1"}`,
		codexCompletedEventBody,
		codexOverloadEvent,
		`{"type":"response.some_future_event_we_have_never_seen"}`,
	}
	for _, payload := range release {
		eventType := gjson.GetBytes([]byte(payload), "type").String()
		if helps.IsCodexBootstrapBufferableEvent(eventType, []byte(payload)) {
			t.Errorf("must release the stream: %s", payload)
		}
	}
}
