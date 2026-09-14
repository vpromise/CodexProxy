package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestLifecycle_DelayedPersistHookAndEventCannotResurrectAuth(t *testing.T) {
	ctx := context.Background()
	m := coreauth.NewManager(nil, nil, nil)
	s := &Service{cfg: &config.Config{}, coreManager: m}
	a, err := m.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "claude", Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	s.applyCoreAuthRemoval(ctx, a.ID)
	if errHook := s.runtimeAuthSyncHook()(ctx, a); errHook != nil {
		t.Fatal(errHook)
	}
	s.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: a.ID, Auth: a})
	s.registerResolvedModelsForAuth(a, "claude", []*ModelInfo{{ID: "claude-test"}})
	if _, exists := m.GetByID(a.ID); exists {
		t.Fatal("stale callback/event recreated deleted auth")
	}
	if models := GlobalModelRegistry().GetModelsForClient(a.ID); len(models) != 0 {
		t.Fatal("stale registration recreated deleted client models")
	}
}

func TestLifecycle_StaleUnsupportedProviderEventCannotRemoveReplacement(t *testing.T) {
	ctx := context.Background()
	m := coreauth.NewManager(nil, nil, nil)
	s := &Service{cfg: &config.Config{}, coreManager: m}
	old, _ := m.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "claude"})
	m.Remove(ctx, old.ID)
	fresh, err := m.Register(ctx, &coreauth.Auth{ID: old.ID, Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Remove(ctx, fresh.ID) })
	old.Provider = "removed-provider"
	s.handleAuthUpdate(ctx, watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: old.ID, Auth: old})
	current, exists := m.GetByID(old.ID)
	if !exists || current.RegistrationEpoch != fresh.RegistrationEpoch || current.Provider != "claude" {
		t.Fatal("stale provider event removed replacement registration")
	}
}

func TestLifecycle_PersistHookCannotRollbackConcurrentRefresh(t *testing.T) {
	ctx := context.Background()
	m := coreauth.NewManager(nil, nil, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	s := &Service{cfg: &config.Config{}, coreManager: m, watcher: &WatcherWrapper{
		dispatchPersistedAuth: func(watcher.AuthUpdate) bool { close(entered); <-release; return true },
	}}
	a, err := m.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "claude", Status: coreauth.StatusActive, Metadata: map[string]any{"access_token": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Remove(ctx, a.ID) })
	done := make(chan error, 1)
	go func() { done <- s.runtimeAuthSyncHook()(ctx, a) }()
	<-entered
	fresh := a.Clone()
	fresh.Metadata["access_token"] = "fresh"
	_, err = m.UpdateRefreshedAuth(ctx, a, fresh)
	close(release)
	if errHook := <-done; errHook != nil {
		t.Fatal(errHook)
	}
	if err != nil {
		t.Fatal(err)
	}
	current, _ := m.GetByID(a.ID)
	if current.Metadata["access_token"] != "fresh" {
		t.Fatal("persistence notification restored an old token")
	}
}
