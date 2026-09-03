package usage

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestNewManagerUsesInitialQueueCapacity(t *testing.T) {
	manager := NewManager(16)
	if got := cap(manager.queue); got != 16 {
		t.Fatalf("queue capacity = %d, want 16", got)
	}
}

func TestManagerStopsWhenStartContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	manager := NewManager(1)
	manager.Start(ctx)
	cancel()

	select {
	case <-manager.stopCh:
	case <-time.After(time.Second):
		t.Fatal("manager did not stop after context cancellation")
	}

	manager.mu.Lock()
	closed := manager.closed
	manager.mu.Unlock()
	if !closed {
		t.Fatal("manager remained open after context cancellation")
	}
}

func TestManagerStartAndStopAreConcurrentSafe(t *testing.T) {
	for i := 0; i < 100; i++ {
		manager := NewManager(1)
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			manager.Start(context.Background())
		}()
		go func() {
			defer workers.Done()
			<-start
			manager.Stop()
		}()
		close(start)
		workers.Wait()
	}
}

func TestGenerateEnabledDefaultsNilToTrue(t *testing.T) {
	if !GenerateEnabled(nil) {
		t.Fatalf("GenerateEnabled(nil) = false, want true")
	}
}

func TestGenerateEnabledHonorsExplicitFalse(t *testing.T) {
	if GenerateEnabled(GenerateFlag(false)) {
		t.Fatalf("GenerateEnabled(false) = true, want false")
	}
}

func TestGenerateEnabledHonorsExplicitTrue(t *testing.T) {
	if !GenerateEnabled(GenerateFlag(true)) {
		t.Fatalf("GenerateEnabled(true) = false, want true")
	}
}

func TestGenerateFromContextDefaultsMissingToTrue(t *testing.T) {
	if !GenerateFromContext(context.Background()) {
		t.Fatalf("GenerateFromContext(background) = false, want true")
	}
}

func TestGenerateFromContextHonorsExplicitFalse(t *testing.T) {
	ctx := WithGenerate(context.Background(), false)
	if GenerateFromContext(ctx) {
		t.Fatalf("GenerateFromContext(false) = true, want false")
	}
}

func TestRecordOmittedGenerateIsEnabled(t *testing.T) {
	// Existing callers construct Record without setting Generate.
	// Omission must remain distinguishable from explicit false and default to true.
	record := Record{
		Provider: "openai",
		Model:    "gpt-5.4",
	}
	if record.Generate != nil {
		t.Fatalf("Record.Generate = %v, want nil for omitted field", record.Generate)
	}
	if !GenerateEnabled(record.Generate) {
		t.Fatalf("GenerateEnabled(omitted) = false, want true")
	}
}
