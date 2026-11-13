package session

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"sync"

	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
)

func TestClaudeThreadAuthMemoryIsBounded(t *testing.T) {
	home.ClearCurrent()
	t.Cleanup(home.ClearCurrent)
	scope := t.Name()
	for i := 0; i <= claudeThreadAuthLimit; i++ {
		RememberClaudeThreadAuth(scope, "message-"+strconv.Itoa(i), "credential-a")
	}
	if _, found, errLookup := ClaudeThreadAuth(context.Background(), scope, "message-0"); errLookup != nil || found {
		t.Fatalf("oldest owner was not evicted: found=%v, error=%v", found, errLookup)
	}
	if authID, found, errLookup := ClaudeThreadAuth(context.Background(), scope, "message-"+strconv.Itoa(claudeThreadAuthLimit)); errLookup != nil || !found || authID != "credential-a" {
		t.Fatalf("newest owner was lost: auth=%q, found=%v, error=%v", authID, found, errLookup)
	}
}

func TestClaudeThreadAuthMemoryExpiration(t *testing.T) {
	home.ClearCurrent()
	t.Cleanup(home.ClearCurrent)
	scope := t.Name()
	RememberClaudeThreadAuth(scope, "message-a", "credential-a")
	key := claudeThreadAuthKey(scope, "message-a")
	claudeThreadAuthCache.Lock()
	entry := claudeThreadAuthCache.entries[key]
	entry.expiresAt = time.Unix(0, 0)
	claudeThreadAuthCache.entries[key] = entry
	claudeThreadAuthCache.Unlock()
	if _, found, errLookup := ClaudeThreadAuth(context.Background(), scope, "message-a"); errLookup != nil || found {
		t.Fatalf("expired owner remained routable: found=%v, error=%v", found, errLookup)
	}
}

func TestClaudeThreadAuthCallerIsolationAndFailure(t *testing.T) {
	home.ClearCurrent()
	t.Cleanup(home.ClearCurrent)
	scope := t.Name()
	RememberClaudeThreadAuth(scope, "message", "owner")
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			owner, found, err := ClaudeThreadAuth(t.Context(), scope, "message")
			if err != nil || !found || owner != "owner" {
				t.Errorf("lookup %q %v %v", owner, found, err)
			}
		})
	}
	wg.Wait()
	if _, found, err := ClaudeThreadAuth(t.Context(), scope+"other", "message"); found || err != nil {
		t.Fatalf("foreign caller %v %v", found, err)
	}
	home.SetCurrent(home.New(config.HomeConfig{Enabled: false}))
	if _, _, err := ClaudeThreadAuth(t.Context(), scope, "missing"); err == nil {
		t.Fatal("unavailable configured Home silently ignored")
	}
}
