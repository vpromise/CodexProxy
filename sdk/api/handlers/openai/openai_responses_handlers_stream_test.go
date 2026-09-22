package openai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func newResponsesStreamTestHandler(t *testing.T) (*OpenAIResponsesAPIHandler, *httptest.ResponseRecorder, *gin.Context, http.Flusher) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	return h, recorder, c, flusher
}

func TestResponsesSSEFramerFiltersPrivateEvents(t *testing.T) {
	for _, codexClient := range []bool{false, true} {
		for _, tc := range []struct {
			name, frame                   string
			alwaysDrop, codexOnly, noData bool
		}{
			{name: "rate limits", frame: "event: codex.rate_limits\ndata: {\"type\":\"codex.rate_limits\"}\n\n", alwaysDrop: true},
			{name: "telemetry data only", frame: "data: {\"type\":\"responsesapi.websocket_timing\"}\n\n", alwaysDrop: true},
			{name: "private event with public payload type", frame: "event: responsesapi.websocket_timing\ndata: {\"type\":\"response.created\"}\n\n", alwaysDrop: true},
			{name: "private payload with public event", frame: "event: response.created\ndata: {\"type\":\"codex.rate_limits\"}\n\n", alwaysDrop: true},
			{name: "event only", frame: "event: responsesapi.websocket_timing\n\n", alwaysDrop: true},
			{name: "non JSON private data", frame: "event: codex.rate_limits\ndata: internal\n\n", alwaysDrop: true},
			{name: "Codex metadata", frame: "event: codex.response.metadata\ndata: {\"type\":\"codex.response.metadata\",\"turn_id\":\"turn-1\"}\n\n", codexOnly: true},
			{name: "future Codex event", frame: "data: {\"type\":\"codex.future_event\"}\n\n", codexOnly: true},
			{name: "multiline metadata", frame: "event: codex.response.metadata\r\ndata: {\"type\":\"codex.response.metadata\",\r\ndata: \"turn_id\":\"turn-1\"}\r\n\r\n", codexOnly: true},
			{name: "future public event", frame: "event: response.future_event\ndata: {\"type\":\"response.future_event\"}\n\n"},
			{name: "done sentinel", frame: "data: [DONE]\n\n"},
			{name: "comment", frame: ": keep-alive\n\n", noData: true},
		} {
			client := "generic/"
			if codexClient {
				client = "codex/"
			}
			t.Run(client+tc.name, func(t *testing.T) {
				framer := &responsesSSEFramer{isCodexClient: codexClient}
				var output bytes.Buffer
				// Executors supply SSE fields or frames. Also exercise buffering of
				// an incomplete JSON payload without treating field names as byte streams.
				if split := strings.Index(tc.frame, `"type"`); split >= 0 {
					split += 3
					framer.WriteChunk(&output, []byte(tc.frame[:split]))
					framer.WriteChunk(&output, []byte(tc.frame[split:]))
				} else {
					framer.WriteChunk(&output, []byte(tc.frame))
				}
				framer.Flush(&output)
				if tc.alwaysDrop || (tc.codexOnly && !codexClient) {
					if output.Len() != 0 || framer.dataFrames != 0 || framer.lastEvent != "" || framer.terminalEvent != "" {
						t.Fatalf("private frame affected client stream: output=%q state=%+v", output.String(), framer)
					}
					return
				}
				if strings.TrimSpace(output.String()) != strings.TrimSpace(tc.frame) {
					t.Fatalf("public frame changed: got %q, want %q", output.String(), tc.frame)
				}
				wantFrames := 1
				if tc.noData {
					wantFrames = 0
				}
				if framer.dataFrames != wantFrames {
					t.Fatalf("dataFrames = %d, want %d", framer.dataFrames, wantFrames)
				}
			})
		}
	}
}

func TestForwardResponsesStreamFiltersPrivateEventsWithoutLosingOutput(t *testing.T) {
	for _, tc := range []struct {
		name, userAgent, originator string
		wantMetadata                bool
	}{
		{name: "generic", userAgent: "OpenAI/Python"},
		{name: "Codex desktop", userAgent: "Codex Desktop/26.803.41515", wantMetadata: true},
		{name: "Codex CLI", userAgent: "codex_cli_rs/0.154.0", wantMetadata: true},
		{name: "Codex originator", originator: "codex_cli_rs", wantMetadata: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, recorder, c, flusher := newResponsesStreamTestHandler(t)
			c.Request.Header.Set("User-Agent", tc.userAgent)
			c.Request.Header.Set("Originator", tc.originator)
			data := make(chan []byte, 1)
			data <- []byte("data: {\"type\":\"codex.rate_limits\"}\n\n" +
				"data: {\"type\":\"responsesapi.websocket_timing\"}\n\n" +
				"data: {\"type\":\"codex.response.metadata\",\"turn_id\":\"turn-1\"}\n\n" +
				"data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"id\":\"item-1\",\"type\":\"message\"}}\n\n" +
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
			close(data)
			errs := make(chan *interfaces.ErrorMessage)
			close(errs)
			var canceled error
			h.forwardResponsesStream(c, flusher, func(err error) { canceled = err }, data, errs, nil)
			body := recorder.Body.String()
			if canceled != nil || strings.Contains(body, "codex.rate_limits") || strings.Contains(body, "responsesapi.") {
				t.Fatalf("stream error or private telemetry leak: error=%v body=%q", canceled, body)
			}
			if strings.Contains(body, "codex.response.metadata") != tc.wantMetadata {
				t.Fatalf("unexpected metadata visibility: %q", body)
			}
			parts := strings.Split(strings.TrimSpace(body), "\n\n")
			completed := strings.TrimPrefix(parts[len(parts)-1], "data: ")
			if gjson.Get(completed, "response.output.0.id").String() != "item-1" {
				t.Fatalf("completed output repair lost: %q", body)
			}
		})
	}
}

func TestResponsesSSEFramerWaitsForEventFieldAfterData(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}

	framer.WriteChunk(&output, []byte(`data: {"response":{"id":"resp-1","status":"completed"}}`))
	if output.Len() != 0 {
		t.Fatalf("framer emitted data before a following event field arrived: %q", output.String())
	}

	framer.WriteChunk(&output, []byte("event: response.completed"))
	if framer.terminalEvent != "response.completed" {
		t.Fatalf("terminal event = %q, want response.completed", framer.terminalEvent)
	}
	got := output.String()
	if !strings.Contains(got, "data: ") || !strings.Contains(got, "event: response.completed") {
		t.Fatalf("framer did not preserve data-before-event fields in one frame: %q", got)
	}
}

func TestResponsesSSEFramerFlushesMultilineDataWithoutDelimiter(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}
	chunk := []byte("event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}")
	framer.WriteChunk(&output, chunk)
	framer.Flush(&output)

	if framer.terminalEvent != "response.completed" || !strings.Contains(output.String(), "response.completed") {
		t.Fatalf("multiline data-only terminal frame was dropped: terminal=%q output=%q", framer.terminalEvent, output.String())
	}
}

func TestResponsesSSEFramerUsesPayloadErrorOverCompletedEvent(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{failureEvent: "response.failed"}
	framer.WriteChunk(&output, []byte("data: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\nevent: response.completed\n\n"))

	if framer.terminalEvent != "response.failed" || strings.Contains(output.String(), "event: response.completed") {
		t.Fatalf("payload error was overridden by completed event: terminal=%q output=%q", framer.terminalEvent, output.String())
	}
	if strings.Count(output.String(), "event: response.failed") != 1 {
		t.Fatalf("payload error output = %q, want one response.failed", output.String())
	}
}

func TestResponsesSSEFramerUsesErrorEventOverPayloadType(t *testing.T) {
	var output bytes.Buffer
	framer := &responsesSSEFramer{}
	framer.WriteChunk(&output, []byte("event: error\ndata: {\"type\":\"provider.error\",\"message\":\"failed\"}\n\n"))
	if framer.terminalEvent != "error" {
		t.Fatalf("terminal event = %q, want error", framer.terminalEvent)
	}

	framer = &responsesSSEFramer{}
	framer.WriteChunk(&output, []byte("data: {\"response\":{\"error\":{\"message\":\"failed\"}}}\n\n"))
	if framer.terminalEvent != "error" {
		t.Fatalf("nested response error terminal event = %q, want error", framer.terminalEvent)
	}
}

func TestForwardResponsesStreamSeparatesDataOnlySSEChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}")
	data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	parts := strings.Split(strings.TrimSpace(body), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), body)
	}

	expectedPart1 := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{}\"}}"
	if parts[0] != expectedPart1 {
		t.Errorf("unexpected first event.\nGot: %q\nWant: %q", parts[0], expectedPart1)
	}

	expectedPart2 := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"function_call\",\"arguments\":\"{}\"}]}}"
	if parts[1] != expectedPart2 {
		t.Errorf("unexpected second event.\nGot: %q\nWant: %q", parts[1], expectedPart2)
	}
}

func TestForwardResponsesStreamRepairsEmptyCompletedOutputFromDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs-1","summary":[]}}`)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{\"cmd\":\"pwd\"}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.1.name").String(); got != "shell" {
		t.Fatalf("expected function_call name to be preserved, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.arguments").String(); got != `{"cmd":"pwd"}` {
		t.Fatalf("expected function_call arguments to be preserved, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMixedIndexedAndUnindexedDoneItems(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"shell","arguments":"{}","status":"completed"}}`)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"message","id":"msg-1","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`)
	data <- []byte(`data: {"type":"response.completed","response":{"id":"resp-1","output":[]}}`)
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 3 {
		t.Fatalf("expected 3 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	payload := strings.TrimPrefix(parts[2], "data: ")
	output := gjson.Get(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("expected repaired completed output with 2 items, got %s", output.Raw)
	}
	if got := gjson.Get(payload, "response.output.0.name").String(); got != "shell" {
		t.Fatalf("expected indexed function_call to be preserved first, got %q in %s", got, payload)
	}
	if got := gjson.Get(payload, "response.output.1.id").String(); got != "msg-1" {
		t.Fatalf("expected unindexed message to be appended, got %q in %s", got, payload)
	}
}

func TestForwardResponsesStreamRepairsMultilineCompletedOutputAsSSEDataLines(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"{}"}}`)
	data <- []byte("data: {\"type\":\"response.completed\",\ndata: \"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	parts := strings.Split(strings.TrimSpace(recorder.Body.String()), "\n\n")
	if len(parts) != 2 {
		t.Fatalf("expected 2 SSE events, got %d. Body: %q", len(parts), recorder.Body.String())
	}

	completedFrame := []byte(parts[1])
	for _, line := range strings.Split(parts[1], "\n") {
		if line != "" && !strings.HasPrefix(line, "data: ") {
			t.Fatalf("expected every completed payload line to be an SSE data line, got %q in %q", line, parts[1])
		}
	}

	payload, ok := responsesSSEDataPayload(completedFrame)
	if !ok {
		t.Fatalf("expected completed frame to contain data payload: %q", parts[1])
	}
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 1 {
		t.Fatalf("expected repaired completed output with 1 item, got %s from %q", output.Raw, payload)
	}
}

func TestForwardResponsesStreamReassemblesSplitSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 3)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("event: response.created")
	data <- []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}")
	data <- []byte("\n")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	wantPrefix := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("unexpected split-event framing.\nGot:        %q\nWant prefix: %q", got, wantPrefix)
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
	}
}

func TestForwardResponsesStreamPreservesValidFullSSEEventChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	chunk := []byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n")
	data <- chunk
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	if !strings.HasPrefix(got, string(chunk)) {
		t.Fatalf("unexpected full-event framing.\nGot:        %q\nWant prefix: %q", got, string(chunk))
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
	}
}

func TestForwardResponsesStreamBuffersSplitDataPayloadChunks(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 2)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	data <- []byte(",\"response\":{\"id\":\"resp-1\"}}")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	wantPrefix := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\n"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("unexpected split-data framing.\nGot:        %q\nWant prefix: %q", got, wantPrefix)
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
	}
}

func TestResponsesSSENeedsLineBreakSkipsChunksThatAlreadyStartWithNewline(t *testing.T) {
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\n")) {
		t.Fatal("expected no injected newline before newline-only chunk")
	}
	if responsesSSENeedsLineBreak([]byte("event: response.created"), []byte("\r\n")) {
		t.Fatal("expected no injected newline before CRLF chunk")
	}
}

func TestForwardResponsesStreamDropsIncompleteTrailingDataChunkOnFlush(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)

	data := make(chan []byte, 1)
	errs := make(chan *interfaces.ErrorMessage)
	data <- []byte("data: {\"type\":\"response.created\"")
	close(data)
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)

	got := recorder.Body.String()
	if strings.Contains(got, `data: {"type":"response.created"`) {
		t.Fatalf("incomplete trailing data was not dropped on flush: %q", got)
	}
	if !strings.Contains(got, "event: error") {
		t.Fatalf("unterminated framing test stream did not end with an error: %q", got)
	}
}
