package helps

import (
	"net/http"
	"testing"
)

func TestTransportCacheRecoveredBuilderRemainsUsable(t *testing.T) {
	cache := NewTransportCache[string](2)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("builder panic did not propagate")
			}
		}()
		_, _ = cache.Get("key", func() (*http.Transport, error) { panic("boom") })
	}()
	if !cache.mu.TryLock() {
		t.Fatal("cache mutex retained after panic")
	}
	cache.mu.Unlock()
	if cache.Len() != 0 {
		t.Fatal("panicked builder populated cache")
	}
	tr, err := cache.Get("key", func() (*http.Transport, error) { return &http.Transport{}, nil })
	if err != nil || tr == nil {
		t.Fatalf("cache unusable: %v", err)
	}
	cache.Purge()
	if cache.Len() != 0 {
		t.Fatal("purge failed")
	}
}
