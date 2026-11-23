package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServerTerminateDrainsClaudeStreamAfterLongUptime(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM cannot be sent to a process on Windows")
	}
	if testing.Short() {
		t.Skip("requires more than 30 seconds of real service uptime")
	}

	releaseTail := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, initialClaudeDrainEvents)
		w.(http.Flusher).Flush()
		select {
		case <-releaseTail:
			_, _ = io.WriteString(w, finalClaudeDrainEvents)
		case <-r.Context().Done():
		}
	}))
	defer upstream.Close()
	defer close(releaseTail)

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	dir := t.TempDir()
	configPath := writeTestServerConfig(t, dir, port)
	configFile, errOpen := os.OpenFile(configPath, os.O_WRONLY|os.O_APPEND, 0)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	_, errWrite := fmt.Fprintf(configFile, "disable-claude-cloak-mode: true\nclaude-api-key:\n  - api-key: synthetic-claude-key\n    base-url: %q\n    proxy-url: direct\n    models:\n      - name: claude-sonnet-4-6\n", upstream.URL)
	errClose := configFile.Close()
	if errWrite != nil || errClose != nil {
		t.Fatalf("write local upstream config: write=%v, close=%v", errWrite, errClose)
	}

	command, stdout, stderr := newServerExitCommandWithTimeout(t, dir, 55*time.Second, "--config", configPath, "--local-model")
	if errStart := command.Start(); errStart != nil {
		t.Fatal(errStart)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	awaitServerExitListener(t, port, done, stdout, stderr)

	requestTimeout, cancelRequest := context.WithTimeout(context.Background(), 42*time.Second)
	defer cancelRequest()
	request, errRequest := http.NewRequestWithContext(requestTimeout, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/v1/messages", port), strings.NewReader(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"shutdown test"}],"stream":true,"max_tokens":16}`))
	if errRequest != nil {
		t.Fatal(errRequest)
	}
	request.Header.Set("Authorization", "Bearer test-server-exit-key")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	response, errResponse := http.DefaultClient.Do(request)
	if errResponse != nil {
		t.Fatal(errResponse)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("expected native Claude stream, got %s: %s", response.Status, body)
	}
	reader := bufio.NewReader(response.Body)
	for {
		line, errRead := reader.ReadString('\n')
		if errRead != nil {
			t.Fatalf("read before the first text delta: %v", errRead)
		}
		if strings.Contains(line, "drain-start") {
			break
		}
	}

	// The backend keeps the response active while the original startup-time deadline elapses.
	time.Sleep(31 * time.Second)
	if errSignal := command.Process.Signal(syscall.SIGTERM); errSignal != nil {
		t.Fatal(errSignal)
	}
	select {
	case errWait := <-done:
		t.Fatalf("server exited before draining the stream: %v\nstdout:\n%s\nstderr:\n%s", errWait, stdout.String(), stderr.String())
	case <-time.After(250 * time.Millisecond):
	}

	releaseTail <- struct{}{}
	tail, errTail := io.ReadAll(reader)
	if errTail != nil || !strings.Contains(string(tail), "drain-end") || !strings.Contains(string(tail), "event: message_stop") {
		t.Fatalf("incomplete stream after SIGTERM: err=%v, tail=%q", errTail, tail)
	}
	if errWait := <-done; errWait != nil {
		t.Fatalf("expected clean shutdown after the stream: %v\nstdout:\n%s\nstderr:\n%s", errWait, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "failed to shutdown HTTP server") ||
		strings.Contains(stderr.String(), "failed to shutdown HTTP server") {
		t.Fatalf("unexpected stop failure\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

const initialClaudeDrainEvents = `event: message_start
data: {"type":"message_start","message":{"id":"msg_drain","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"drain-start"}}

`
const finalClaudeDrainEvents = `event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"drain-end"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}

event: message_stop
data: {"type":"message_stop"}

`
