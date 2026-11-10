package auth

import (
	"context"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestPoolEgressSnapshotAndSessionReauthorization(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	account := &Auth{ID: "synthetic", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-principal"}}
	if _, err := manager.Register(context.Background(), account); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true,
		Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{AccountIdentity(account)}, Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "127.0.0.1"}}},
		Keys:  []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}},
	}}
	cfg.APIKeys = []string{"synthetic-client"}
	manager.SetConfig(cfg)
	manager.PublishAccessPolicy(cfg)
	caller := WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
	active := manager.NewAccessRequest(caller)
	cfg.AccessIsolation.Pools[0].Egress.SourceIP = "127.0.0.2"
	if got := proxyutil.EgressFromContext(manager.NewAccessRequest(caller)).SourceIP; got != "127.0.0.1" {
		t.Fatal("published policy retained mutable configuration")
	}
	manager.PublishAccessPolicy(cfg)
	if got := proxyutil.EgressFromContext(active).SourceIP; got != "127.0.0.1" {
		t.Fatal("active operation changed egress")
	}
	if got := proxyutil.EgressFromContext(manager.NewAccessRequest(caller)).SourceIP; got != "127.0.0.2" {
		t.Fatal("new operation retained old egress")
	}
	if err := manager.CheckCurrentAccount(active, account); err == nil {
		t.Fatal("existing socket used its old exit after pool update")
	}
	current := manager.NewAccessRequest(caller)
	if err := manager.CheckCurrentAccount(current, account); err != nil {
		t.Fatal(err)
	}
	manager.SetConfig(&config.Config{})
	if got := proxyutil.EgressFromContext(manager.NewAccessRequest(caller)).SourceIP; got != "127.0.0.2" {
		t.Fatal("stale executor reload restored an older exit")
	}
	denied := manager.NewAccessRequest(WithClientIdentity(active, "unknown-key", "config-inline"))
	if route := proxyutil.EgressFromContext(denied); route.SourceIP != "" {
		t.Fatal("denied caller retained its parent's route")
	}
	manager.PublishAccessPolicy(&config.Config{})
	if route := proxyutil.EgressFromContext(manager.NewAccessRequest(active)); route.SourceIP != "" {
		t.Fatal("disabled isolation retained a pool route")
	}
}

func TestPoolEgressCredentialFailuresKeepCapturedRoute(t *testing.T) {
	for _, routeFailure := range []bool{false, true} {
		t.Run(fmt.Sprint(routeFailure), func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.SetRetryConfig(0, 0, 0)
			accounts := []*Auth{
				{ID: "egress-first", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-first"}},
				{ID: "egress-second", Provider: "codex", Metadata: map[string]any{"account_id": "synthetic-second"}},
			}
			for _, account := range accounts {
				if _, err := manager.Register(context.Background(), account); err != nil {
					t.Fatal(err)
				}
				registry.GetGlobalRegistry().RegisterClient(account.ID, "codex", []*registry.ModelInfo{{ID: "synthetic-egress-model"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(account.ID) })
			}
			cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true,
				Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{AccountIdentity(accounts[0]), AccountIdentity(accounts[1])}, Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "127.0.0.1"}}},
				Keys:  []config.ClientKeyBinding{{Key: "synthetic-client", Pool: "one"}},
			}}
			cfg.APIKeys = []string{"synthetic-client"}
			manager.SetConfig(cfg)
			attempts := 0
			manager.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: func(ctx context.Context, _ *Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
				attempts++
				route := proxyutil.EgressFromContext(ctx)
				if route.SourceIP != "127.0.0.1" || route.Scope != "one" {
					t.Fatal("credential failover lost its pool route")
				}
				if routeFailure {
					return coreexecutor.Response{}, &proxyutil.EgressError{Err: fmt.Errorf("synthetic source unavailable")}
				}
				if attempts == 1 {
					return coreexecutor.Response{}, &Error{Message: "synthetic upstream failure", HTTPStatus: 503}
				}
				return coreexecutor.Response{Payload: []byte("ok")}, nil
			}}})
			caller := WithClientIdentity(context.Background(), "synthetic-client", "config-inline")
			_, err := manager.Execute(caller, []string{"codex"}, coreexecutor.Request{Model: "synthetic-egress-model"}, coreexecutor.Options{})
			if routeFailure {
				if err == nil || attempts != 1 {
					t.Fatalf("route failure cycled credentials: attempts=%d err=%v", attempts, err)
				}
			} else if err != nil || attempts != 2 {
				t.Fatalf("credential failover failed: attempts=%d err=%v", attempts, err)
			}
		})
	}
}
