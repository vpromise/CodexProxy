package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func codexStreamChunks(t *testing.T, stream string, buffered bool) []string {
	t.Helper()
	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = buffered
	executor := NewCodexExecutor(cfg)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		if len(chunk.Payload) > 0 {
			chunks = append(chunks, string(chunk.Payload))
		}
	}
	return chunks
}

func TestCodexExecuteStreamTreatsAnEventSplitAcrossDataLinesAsOneEvent(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp_split","model":"gpt-5.4","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`
	cut := strings.Index(completed, `"output":`)
	// With bootstrap buffering, the first output releases the stream before the completed event.
	created := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_split\",\"model\":\"gpt-5.4\"}}\n\n" +
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hi\"}\n\n"
	oneLine := created + "event: response.completed\ndata: " + completed + "\n\n"
	split := created + "event: response.completed\ndata: " + completed[:cut] + "\ndata: " + completed[cut:] + "\n\n"
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			want := codexStreamChunks(t, oneLine, buffered)
			got := codexStreamChunks(t, split, buffered)
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", want) {
				t.Fatalf("split event chunks = %q\nwant the one-line chunks %q", got, want)
			}
			if terminal := want[len(want)-1]; !strings.Contains(terminal, `"response.completed"`) || !json.Valid([]byte(strings.TrimPrefix(terminal, "data: "))) {
				t.Fatalf("last chunk = %q, want a valid response.completed", terminal)
			}
		})
	}
}

func cloneLogHooks(src log.LevelHooks) log.LevelHooks {
	cloned := make(log.LevelHooks, len(src))
	for lvl, hooks := range src {
		cloned[lvl] = append([]log.Hook(nil), hooks...)
	}
	return cloned
}

func TestCodexExecuteStreamPassesADoneSentinelAfterTheCompletedEvent(t *testing.T) {
	origHooks := cloneLogHooks(log.StandardLogger().Hooks)
	hook := new(logtest.Hook)
	log.StandardLogger().AddHook(hook)
	t.Cleanup(func() { log.StandardLogger().ReplaceHooks(origHooks) })

	stream := codexAllocationStream(2, 16) + "data: [DONE]\n\n"
	chunks := codexStreamChunks(t, stream, false)
	sawCompleted := false
	for _, chunk := range chunks {
		if strings.Contains(chunk, `"response.completed"`) {
			sawCompleted = true
		}
	}
	if !sawCompleted {
		t.Fatalf("chunks = %q, want the completed event", chunks)
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "SSE event data is not valid JSON") {
			t.Fatalf("unexpected framing warning: %s", entry.Message)
		}
	}
}

func TestCodexExecuteNonStreamReadsAnEventSplitAcrossDataLines(t *testing.T) {
	completed := `{"type":"response.completed","response":{"id":"resp_split","object":"response","model":"gpt-5.4","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}`
	cut := strings.Index(completed, `"output":`)
	stream := "event: response.completed\r\ndata: " + completed[:cut] + "\r\ndata: " + completed[cut:] + "\r\n\r\n"
	executor := NewCodexExecutor(&config.Config{})
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	resp, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	var got struct {
		ID     string            `json:"id"`
		Output []json.RawMessage `json:"output"`
	}
	if errDecode := json.Unmarshal(resp.Payload, &got); errDecode != nil || got.ID != "resp_split" || len(got.Output) != 1 {
		t.Fatalf("payload = %s (decode error %v), want the completed response", resp.Payload, errDecode)
	}
}

func TestCodexExecuteStreamKeepsAnInvalidSplitEventInvalid(t *testing.T) {
	// Split inside a string, so the SSE-joined data has a raw newline in it and is not JSON.
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_bad\",\"model\":\"gpt-5.4\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_bad\",\"status\":\"comp\ndata: leted\",\"output\":[]}}\n\n"
	chunks := codexStreamChunks(t, stream, false)
	last := chunks[len(chunks)-1]
	var values []string
	for _, line := range strings.Split(last, "\n") {
		values = append(values, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
	}
	if !strings.HasPrefix(last, "data: ") || strings.Count(last, "\ndata: ") != 1 || json.Valid([]byte(strings.Join(values, "\n"))) {
		t.Fatalf("last chunk = %q, want the invalid event's two data lines unchanged", last)
	}
}

func TestCodexExecuteStreamAssemblesSplitEventsWithAndWithoutBootstrapBuffering(t *testing.T) {
	// A handshake event split across three data lines, and a terminal event split across two data lines.
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_split\",\"model\":\"gpt-5.4\"}}\n\n" +
		"data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_split\",\"output\":[\ndata: {\"x\":1},\ndata: {\"x\":1}]}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_split\",\"status\":\"completed\",\ndata: \"output\":[]}}\n\n"
	fragments := func(chunks []string) int {
		count := 0
		for _, chunk := range chunks {
			if chunk == `data: {"x":1},` {
				count++
			}
		}
		return count
	}
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			chunks := codexStreamChunks(t, stream, buffered)
			if count := fragments(chunks); count != 0 {
				t.Fatalf("buffered=%t: %d fragments emitted, want the event assembled", buffered, count)
			}
			last := chunks[len(chunks)-1]
			if !strings.Contains(last, `"response.completed"`) || !json.Valid([]byte(strings.TrimPrefix(last, "data: "))) {
				t.Fatalf("last chunk = %q, want a valid response.completed", last)
			}
		})
	}
}

func TestCodexExecuteStreamMinimalReproFromIssue6575(t *testing.T) {
	// Minimal reproduction from issue 6575: response.completed immediately follows response.created and is split.
	stream := "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\ndata: \"output\":[]}}\n\n"
	for _, buffered := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffered=%t", buffered), func(t *testing.T) {
			chunks := codexStreamChunks(t, stream, buffered)
			if len(chunks) == 0 {
				t.Fatal("expected chunks, got none")
			}
			last := chunks[len(chunks)-1]
			if !strings.Contains(last, `"response.completed"`) || !json.Valid([]byte(strings.TrimPrefix(last, "data: "))) {
				t.Fatalf("last chunk = %q, want valid response.completed", last)
			}
		})
	}
}

func TestCodexExecuteStreamSplitHandshakeExceedingLineBudgetReleasesBootstrapWithoutFailover(t *testing.T) {
	// A split in_progress event that spans 50 data lines (> codexBootstrapMaxBufferedFrames of 48), followed by overload.
	var stream strings.Builder
	stream.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_split\",\"model\":\"gpt-5.4\"}}\n\n")
	stream.WriteString("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_split\",\"items\":[\n")
	for i := 0; i < 50; i++ {
		stream.WriteString("data: \n")
	}
	stream.WriteString("data: ]}}\n\n")
	// Followed by overload error
	stream.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_overloaded\",\"message\":\"The server had an error while processing your request.\",\"param\":\"input\"}}\n\n")

	// When bootstrap buffering is enabled, the 50 lines exceeded the line budget (48 frames),
	// so the stream is released and the overload error is delivered in-stream rather than failing over.
	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = true
	executor := NewCodexExecutor(cfg)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream.String()})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("expected stream to be released without failover error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil stream result")
	}
	sawError := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected in-stream delivery of overload error after budget exhausted")
	}
}

func TestCodexExecuteStreamEventExceedsBudgetWhileAssemblingReleasesStream(t *testing.T) {
	// Line 1: event: response.created
	// Line 2: data: {"type":"response.created",...}
	// Line 3: blank line
	// Line 4: data: {"type":"response.in_progress",...
	// Lines 5-47: 43 empty data lines
	// Line 48: {"key_at_boundary": 123} - exact 48th physical line triggering budget limit
	// Lines 49-50: {"key_after_boundary": 456} and ]}} closing event
	var stream strings.Builder
	stream.WriteString("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_split\",\"model\":\"gpt-5.4\"}}\n\n")
	stream.WriteString("data: {\"type\":\"response.in_progress\",\"response\":{\"id\":\"resp_split\",\"items\":[\n")
	for i := 0; i < 43; i++ {
		stream.WriteString("data: \n")
	}
	// Line 48: triggers the 48-line frame budget limit during assembly.
	stream.WriteString("data: {\"key_at_boundary\":123},\n")
	// Lines 49-50: streamed in goroutine after bootstrap release.
	stream.WriteString("data: {\"key_after_boundary\":456}\n")
	stream.WriteString("data: ]}}\n") // without blank separator, next line is error
	stream.WriteString("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"code\":\"server_overloaded\",\"message\":\"overloaded\"}}\n\n")

	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = true
	executor := NewCodexExecutor(cfg)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream.String()})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("expected stream to be released without failover error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil stream result")
	}
	sawError := false
	var inProgressChunk string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawError = true
		} else if strings.Contains(string(chunk.Payload), "response.in_progress") {
			inProgressChunk = string(chunk.Payload)
		}
	}
	if !sawError {
		t.Fatal("expected in-stream delivery of overload error after budget exhausted")
	}
	if !strings.Contains(inProgressChunk, `"key_at_boundary":123`) || !strings.Contains(inProgressChunk, `"key_after_boundary":456`) {
		t.Fatalf("inProgressChunk = %q, want both key_at_boundary and key_after_boundary preserved", inProgressChunk)
	}
	if !json.Valid([]byte(strings.TrimPrefix(inProgressChunk, "data: "))) {
		t.Fatalf("inProgressChunk = %q, want valid JSON without dropped lines", inProgressChunk)
	}
}

func TestCodexExecuteStreamMixedFramingCommentInsideSplitEventCountsBudget(t *testing.T) {
	// 46 data lines held in an in_progress event, then 2 comment lines (total 48 lines), then overload.
	var stream strings.Builder
	stream.WriteString("data: {\"type\":\"response.in_progress\",\"items\":[\n")
	for i := 0; i < 46; i++ {
		stream.WriteString("data: \n")
	}
	// 47th line: comment inside the split event
	stream.WriteString(": keepalive-1\n")
	// 48th line: comment inside the split event (hits budget limit)
	stream.WriteString(": keepalive-2\n")
	// 49th line: overload without failover
	stream.WriteString("event: error\ndata: " + codexOverloadEvent + "\n\n")
	// finishes event
	stream.WriteString("data: ]}}\n\n")

	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = true
	executor := NewCodexExecutor(cfg)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream.String()})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("expected stream to be released without failover error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil stream result")
	}
	sawError := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected in-stream delivery of overload error after mixed framing budget exhausted")
	}
}

func TestCodexExecuteStreamEventExceedsByteBudgetWhileAssemblingReleasesStream(t *testing.T) {
	// A split event whose data lines exceed codexBootstrapMaxBufferedBytes (1 MiB) while assembling,
	// followed by overload.
	var stream strings.Builder
	stream.WriteString("data: {\"type\":\"response.in_progress\",\"items\":[\n")
	// Write 5 data lines of 250KB each = 1.25MB > 1MB limit
	largeChunk := strings.Repeat("x", 250*1024)
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&stream, "data: %q,\n", largeChunk)
	}
	stream.WriteString("data: \"last\"]}\n\n")
	stream.WriteString("event: error\ndata: " + codexOverloadEvent + "\n\n")

	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = true
	executor := NewCodexExecutor(cfg)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", codexAllocationTransport{stream: stream.String()})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test"}}
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(`{"model":"gpt-5.4","input":"hello"}`)},
		cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("expected stream to be released without failover error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil stream result")
	}
	sawError := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawError = true
		}
	}
	if !sawError {
		t.Fatal("expected in-stream delivery of overload error after byte budget exhausted")
	}
}
