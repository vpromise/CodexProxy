package usage

import (
	"context"
	"github.com/gin-gonic/gin"
	"sync"
	"testing"
	"time"
)

func TestCapturedAPIKeyDetachedAndEmpty(t *testing.T) {
	var nilGin *gin.Context
	for _, ctx := range []context.Context{nil, context.Background(), context.WithValue(context.Background(), "gin", nilGin)} {
		captured := CaptureAPIKey(ctx)
		if key, ok := CapturedAPIKeyFromContext(captured); !ok || key != "" {
			t.Fatalf("empty capture = %q, %v", key, ok)
		}
	}
	parent := WithCapturedAPIKey(nil, "parent")
	child := WithCapturedAPIKey(parent, "")
	SetCapturedAPIKey(parent, "home-parent")
	if key, ok := CapturedAPIKeyFromContext(child); !ok || key != "" {
		t.Fatalf("child mutated: %q, %v", key, ok)
	}
	if CaptureAPIKey(child) != child {
		t.Fatal("capture discarded an existing empty snapshot")
	}
}

func TestCapturedAPIKeyConcurrentHomeUpdate(t *testing.T) {
	ctx := WithCapturedAPIKey(nil, "initial")
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			SetCapturedAPIKey(ctx, "home")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			key, ok := CapturedAPIKeyFromContext(ctx)
			if !ok || (key != "initial" && key != "home") {
				t.Errorf("unexpected snapshot: %q, %v", key, ok)
				return
			}
		}
	}()
	wg.Wait()
}

type snapshotUsagePlugin struct {
	release <-chan struct{}
	got     chan string
}

func (p snapshotUsagePlugin) HandleUsage(ctx context.Context, record Record) {
	<-p.release
	key, ok := CapturedAPIKeyFromContext(ctx)
	if !ok {
		key = "missing snapshot"
	}
	p.got <- key
}

func TestQueuedUsageIdentityMatchesRecordAfterHomeUpdate(t *testing.T) {
	m := NewManager(2)
	defer m.Stop()
	release := make(chan struct{})
	got := make(chan string, 2)
	m.Register(snapshotUsagePlugin{release: release, got: got})
	ctx := WithCapturedAPIKey(nil, "initial")
	m.Publish(ctx, Record{APIKey: "record-key"})
	m.Publish(ctx, Record{APIKey: ""})
	SetCapturedAPIKey(ctx, "next-home")
	close(release)
	for _, want := range []string{"record-key", ""} {
		select {
		case key := <-got:
			if key != want {
				t.Fatalf("queued key = %q, want %q", key, want)
			}
		case <-time.After(time.Second):
			t.Fatal("usage delivery stalled")
		}
	}
}
