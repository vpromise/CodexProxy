package handlers

import (
	"context"
	"errors"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestNextStreamChunkReportsCancellationWhenTheChannelClosesWithIt(t *testing.T) {
	for range 200 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		chunks := make(chan coreexecutor.StreamChunk)
		close(chunks)
		if _, ok, canceled := nextStreamChunk(ctx, nil, nil, chunks); ok || !canceled {
			t.Fatalf("closed channel on a canceled context: ok=%v canceled=%v, want a cancellation", ok, canceled)
		}
	}
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	if _, ok, canceled := nextStreamChunk(context.Background(), nil, nil, chunks); ok || canceled {
		t.Fatalf("closed channel on a live context: ok=%v canceled=%v, want the end of the stream", ok, canceled)
	}
}

func TestNextStreamChunkPreservesBufferedErrorsAndCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancel()
	want := errors.New("buffered terminal failure")
	pending := []coreexecutor.StreamChunk{{Err: want}}
	closed := false
	chunk, ok, canceled := nextStreamChunk(ctx, &pending, &closed, nil)
	if !ok || canceled || chunk.Err != want || len(pending) != 0 || closed {
		t.Fatalf("pending terminal error was lost: chunk=%+v ok=%v canceled=%v closed=%v", chunk, ok, canceled, closed)
	}
	if _, ok, canceled = nextStreamChunk(ctx, nil, nil, nil); ok || !canceled {
		t.Fatalf("canceled context with nil channel: ok=%v canceled=%v", ok, canceled)
	}
	chunks := make(chan coreexecutor.StreamChunk)
	close(chunks)
	if _, ok, canceled = nextStreamChunk(nil, nil, &closed, chunks); ok || canceled || !closed {
		t.Fatalf("nil context EOF: ok=%v canceled=%v closed=%v", ok, canceled, closed)
	}
	if _, ok, canceled = nextStreamChunk(ctx, nil, &closed, nil); ok || canceled {
		t.Fatalf("previously observed EOF changed: ok=%v canceled=%v", ok, canceled)
	}
}
