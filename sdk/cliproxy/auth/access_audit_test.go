package auth

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

func TestAccountPoolAuditRecordsSelectionAndRevocationWithoutSecrets(t *testing.T) {
	hook := setupTestLoggerHook(t)
	log.SetLevel(log.InfoLevel)
	m := NewManager(nil, &RoundRobinSelector{}, nil)
	a := &Auth{ID: "private-auth-file", Provider: "codex", Metadata: map[string]any{
		"account_id": "private-account-id", "access_token": "private-token", "email": "private@example.invalid",
	}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "audit-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	attempts := 0
	m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", mockCustomErrorExecutor: mockCustomErrorExecutor{executeFn: func(context.Context, *Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
		attempts++
		return coreexecutor.Response{}, nil
	}}})
	cfg := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true,
		Pools: []config.AccountPool{{ID: "private-pool-name", Provider: "codex", Accounts: []string{AccountIdentity(a)}}},
		Keys:  []config.ClientKeyBinding{{Key: "private-client-key", Pool: "private-pool-name"}},
	}}
	cfg.APIKeys = []string{"private-client-key"}
	m.SetConfig(cfg)
	ctx := WithClientIdentity(logging.WithRequestID(context.Background(), "audit-request"), "private-client-key", "config-inline")
	if _, err := m.Execute(ctx, []string{"codex"}, coreexecutor.Request{Model: "audit-model", Payload: []byte("private-prompt")}, coreexecutor.Options{}); err != nil {
		t.Fatal(err)
	}
	oldRevision := m.accessPolicy.Load().Revision
	cfg.AccessIsolation.Keys = nil
	m.PublishAccessPolicy(cfg)
	if _, err := m.Execute(ctx, []string{"codex"}, coreexecutor.Request{Model: "audit-model"}, coreexecutor.Options{}); err == nil {
		t.Fatal("revocation accepted")
	}
	if attempts != 1 {
		t.Fatalf("upstream attempts = %d", attempts)
	}
	selected, denied := 0, 0
	for _, entry := range hook.AllEntries() {
		if entry.Message != "account isolation decision" {
			continue
		}
		formatted, err := (&logging.LogFormatter{}).Format(entry)
		if err != nil {
			t.Fatal(err)
		}
		line := string(formatted)
		for _, secret := range []string{"private-auth-file", "private-account-id", "private-token", "private@example.invalid", "private-client-key", "private-pool-name", "private-prompt"} {
			if strings.Contains(line, secret) {
				t.Fatal("audit exposed private data")
			}
		}
		for _, field := range []string{"access_event=", "access_operation=", "client_ref=", "pool_ref=", "policy_revision=", "audit-request"} {
			if !strings.Contains(line, field) {
				t.Fatalf("missing audit field %s", field)
			}
		}
		if entry.Data["access_event"] == "selected" {
			selected++
			if entry.Data["account_ref"] != AccountIdentity(a) || entry.Data["policy_revision"] != oldRevision {
				t.Fatal("selection evidence does not match actual attempt")
			}
		}
		if entry.Data["access_event"] == "denied" {
			denied++
			if entry.Data["policy_revision"] == oldRevision {
				t.Fatal("revocation logged old revision")
			}
		}
	}
	if selected != 1 || denied == 0 {
		t.Fatalf("audit selected=%d denied=%d", selected, denied)
	}
}

func TestPublishedAccessPolicySurvivesStaleRuntimeAndKeepsActiveGrant(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a := &Auth{Provider: "codex", Metadata: map[string]any{"account_id": "synthetic"}}
	old := &config.Config{AccessIsolation: config.AccessIsolation{Enabled: true,
		Pools: []config.AccountPool{{ID: "pool", Provider: "codex", Accounts: []string{AccountIdentity(a)}}},
		Keys:  []config.ClientKeyBinding{{Key: "client", Pool: "pool"}},
	}}
	old.APIKeys = []string{"client"}
	m.SetConfig(old)
	caller := WithClientIdentity(context.Background(), "client", "config-inline")
	active := m.NewAccessRequest(caller)
	next := old.CloneForRuntime()
	next.AccessIsolation.Pools[0].Accounts = nil
	m.PublishAccessPolicy(next)
	m.SetConfig(old)
	if AccessScopeFromContext(m.NewAccessRequest(caller)).Allows(a) {
		t.Fatal("stale runtime reopened grant")
	}
	if !AccessScopeFromContext(active).Allows(a) {
		t.Fatal("in-progress grant interrupted")
	}
	// Published snapshots must also be independent of caller-owned slices.
	next.AccessIsolation.Pools[0].Accounts = []string{AccountIdentity(a)}
	if AccessScopeFromContext(m.NewAccessRequest(caller)).Allows(a) {
		t.Fatal("policy mutated through caller config")
	}
}
