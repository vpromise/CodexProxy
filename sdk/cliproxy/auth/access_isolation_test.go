package auth

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAccountPoolAuthorizationAtExecutionBoundary(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	a := &Auth{ID: "pool-account-a", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-a"}}
	b := &Auth{ID: "pool-account-b", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-b"}}
	for _, account := range []*Auth{a, b} {
		if _, err := manager.Register(context.Background(), account); err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(account.ID, "codex", []*registry.ModelInfo{{ID: "synthetic-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(account.ID) })
	}
	var attempts []string
	manager.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: func(_ context.Context, a *Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
		attempts = append(attempts, a.ID)
		return coreexecutor.Response{Payload: []byte(a.ID)}, nil
	}}})
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{AccountIdentity(a)}}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}}}}
	cfg.APIKeys = []string{"synthetic-client"}
	manager.SetConfig(cfg)
	ctx := WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
	for i := 0; i < 4; i++ {
		response, err := manager.Execute(ctx, []string{"codex"}, coreexecutor.Request{Model: "synthetic-model"}, coreexecutor.Options{})
		if err != nil || string(response.Payload) != a.ID {
			t.Fatalf("execution escaped pool: %s %v", response.Payload, err)
		}
	}
	if _, err := manager.Execute(ctx, []string{"codex"}, coreexecutor.Request{Model: "synthetic-model"}, coreexecutor.Options{Metadata: map[string]any{coreexecutor.PinnedAuthMetadataKey: b.ID}}); err == nil {
		t.Fatal("foreign pin accepted")
	}
	if _, err := manager.SelectAuth(ctx, "claude", "", coreexecutor.Options{}); err == nil {
		t.Fatal("foreign provider accepted")
	}
	before := len(attempts)
	cfg = cfg.CloneForRuntime()
	cfg.APIKeys = nil
	manager.SetConfig(cfg)
	if _, err := manager.Execute(ctx, []string{"codex"}, coreexecutor.Request{Model: "synthetic-model"}, coreexecutor.Options{}); err == nil {
		t.Fatal("revoked key accepted")
	}
	if len(attempts) != before {
		t.Fatal("denied request contacted upstream")
	}
}

func TestDerivedAccessCannotExpandAfterIssuerReassignment(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-a"}}
	b := &Auth{Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-b"}}
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{
		{ID: "one", Name: "One", Provider: "codex", Accounts: []string{AccountIdentity(a)}},
		{ID: "two", Name: "Two", Provider: "codex", Accounts: []string{AccountIdentity(b)}},
	}, Keys: []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}}}}
	cfg.APIKeys = []string{"synthetic-client"}
	m.SetConfig(cfg)
	caller := WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
	active := m.NewAccessRequest(caller)
	derived := WithAccessCeiling(caller, AccessScopeFromContext(active))
	cfg = cfg.CloneForRuntime()
	cfg.AccessIsolation.Keys[0].Pool = "two"
	m.SetConfig(cfg)
	if !AccessScopeFromContext(active).Allows(a) {
		t.Fatal("in-progress grant changed")
	}
	if AccessScopeFromContext(m.NewAccessRequest(caller)).Allows(a) {
		t.Fatal("new request retained old grant")
	}
	if AccessScopeFromContext(m.NewAccessRequest(derived)).Allows(b) {
		t.Fatal("derived credential expanded its grant")
	}
}

func TestOverlappingPoolsDoNotShareCallerSuppliedSessionBindings(t *testing.T) {
	selector := NewSessionAffinitySelector(&RoundRobinSelector{})
	t.Cleanup(selector.Stop)
	m := NewManager(nil, selector, nil)
	m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex"})
	a := &Auth{ID: "overlap-a", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-a"}}
	b := &Auth{ID: "overlap-b", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-b"}}
	for _, account := range []*Auth{a, b} {
		if _, err := m.Register(context.Background(), account); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "shared", Name: "Shared", Provider: "codex", Accounts: []string{AccountIdentity(a), AccountIdentity(b)}}}, Keys: []config.ClientKeyBinding{{Key: "client-a", Pool: "shared"}, {Key: "client-b", Pool: "shared"}}}}
	cfg.APIKeys = []string{"client-a", "client-b"}
	m.SetConfig(cfg)
	pick := func(key string) string {
		t.Helper()
		selected, err := m.SelectAuth(WithClientIdentity(context.Background(), key, "config-inline"), "codex", "", coreexecutor.Options{Headers: http.Header{"X-Session-Id": []string{"same-untrusted-hint"}}})
		if err != nil {
			t.Fatal(err)
		}
		return selected.ID
	}
	first, second := pick("client-a"), pick("client-b")
	if first == second {
		t.Fatal("second caller inherited first caller's session binding")
	}
	if pick("client-a") != first || pick("client-b") != second {
		t.Fatal("caller-local affinity was not preserved")
	}
}
