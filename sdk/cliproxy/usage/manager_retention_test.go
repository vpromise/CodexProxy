package usage

import (
	"context"
	"runtime"
	"testing"
	"time"
	"weak"
)

type retentionPayload struct {
	data [4096]byte
}

type retentionContextKey struct{}

type retentionPlugin struct {
	t        *testing.T
	advanced chan struct{}
	release  chan struct{}
}

func (p *retentionPlugin) HandleUsage(ctx context.Context, record Record) {
	if record.Model == "sentinel" {
		close(p.advanced)
		<-p.release
		return
	}
	payload, ok := ctx.Value(retentionContextKey{}).(*retentionPayload)
	if !ok || payload.data[0] != 42 || record.Model != "fixture" {
		p.t.Error("usage plugin did not receive the original record and context")
	}
}

func publishRetentionFixture(manager *Manager) weak.Pointer[retentionPayload] {
	payload := &retentionPayload{}
	payload.data[0] = 42
	ref := weak.Make(payload)
	manager.Publish(context.WithValue(context.Background(), retentionContextKey{}, payload), Record{Model: "fixture"})
	return ref
}

func TestManagerReleasesDispatchedContextWhileStillRunning(t *testing.T) {
	manager := NewManager(16)
	defer manager.Stop()
	plugin := &retentionPlugin{t: t, advanced: make(chan struct{}), release: make(chan struct{})}
	defer close(plugin.release)
	manager.Register(plugin)
	ref := publishRetentionFixture(manager)
	manager.Publish(context.Background(), Record{Model: "sentinel"})
	select {
	case <-plugin.advanced:
	case <-time.After(5 * time.Second):
		t.Fatal("usage dispatcher did not advance past the completed record")
	}
	// Keep the manager and its partially consumed backing array alive. The next
	// callback is blocked, so collection cannot depend on shutdown or rollover.
	defer runtime.KeepAlive(manager)
	for i := 0; i < 20; i++ {
		runtime.GC()
		if ref.Value() == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("completed usage record still retains its request context")
}
