package pluginhost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

type modelStreamBridge struct {
	next    atomic.Uint64
	mu      sync.Mutex
	streams map[string]modelStreamEntry
}

type modelStreamEntry struct {
	pluginID        string
	ownerCallbackID string
	chunks          <-chan handlers.ModelExecutionChunk
	cancel          context.CancelFunc
}

func newModelStreamBridge() *modelStreamBridge {
	return &modelStreamBridge{streams: make(map[string]modelStreamEntry)}
}

func (b *modelStreamBridge) open(pluginID, ownerCallbackID string, chunks <-chan handlers.ModelExecutionChunk, cancel context.CancelFunc) string {
	if b == nil || chunks == nil {
		if cancel != nil {
			cancel()
		}
		return ""
	}
	id := strconv.FormatUint(b.next.Add(1), 10)
	b.mu.Lock()
	b.streams[id] = modelStreamEntry{
		pluginID:        strings.TrimSpace(pluginID),
		ownerCallbackID: ownerCallbackID,
		chunks:          chunks,
		cancel:          cancel,
	}
	b.mu.Unlock()
	return id
}

func (b *modelStreamBridge) read(ctx context.Context, pluginID, id string) (handlers.ModelExecutionChunk, bool, error) {
	if b == nil {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream bridge is unavailable")
	}
	id = strings.TrimSpace(id)
	pluginID = strings.TrimSpace(pluginID)
	if id == "" {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream id is required")
	}
	b.mu.Lock()
	entry, ok := b.streams[id]
	b.mu.Unlock()
	if !ok || entry.chunks == nil {
		return handlers.ModelExecutionChunk{}, true, nil
	}
	if entry.pluginID != pluginID {
		return handlers.ModelExecutionChunk{}, true, fmt.Errorf("model stream %s is not open", id)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		b.close(pluginID, id)
		return handlers.ModelExecutionChunk{}, true, ctx.Err()
	case chunk, okRead := <-entry.chunks:
		if !okRead {
			b.close(pluginID, id)
			return handlers.ModelExecutionChunk{}, true, nil
		}
		if chunk.Err != nil {
			b.close(pluginID, id)
			return chunk, true, nil
		}
		return chunk, false, nil
	}
}

func (b *modelStreamBridge) close(pluginID, id string) {
	id = strings.TrimSpace(id)
	pluginID = strings.TrimSpace(pluginID)
	if b == nil || id == "" {
		return
	}
	b.mu.Lock()
	entry, ok := b.streams[id]
	if !ok || entry.pluginID != pluginID {
		b.mu.Unlock()
		return
	}
	delete(b.streams, id)
	b.mu.Unlock()
	if entry.cancel != nil {
		entry.cancel()
	}
}
