package cache

import (
	"sync"
	"time"
)

const replayCacheCleanupInterval = 10 * time.Minute

var cacheCleanupOnce sync.Once

func startCacheCleanup() {
	go func() {
		ticker := time.NewTicker(replayCacheCleanupInterval)
		defer ticker.Stop()
		for now := range ticker.C {
			purgeExpiredCodexReasoningReplayCache(now)
			purgeExpiredClaudeThinkingReplayCache(now)
		}
	}()
}
