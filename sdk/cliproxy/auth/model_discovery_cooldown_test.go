package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestManager_ModelDiscoveryAfterTransientCooldown(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	id, model := t.Name(), t.Name()+"-model"
	if _, err := m.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model, Object: "model"}})
	t.Cleanup(func() { reg.UnregisterClient(id) })
	hint := time.Nanosecond
	m.MarkResult(ctx, Result{AuthID: id, Provider: "claude", Model: model, Error: &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream capacity"}, RetryAfter: &hint})
	current, _ := m.GetByID(id)
	if blocked, _, _ := isAuthBlockedForModel(current, model, time.Now()); blocked {
		t.Fatal("expired cooldown still blocks the credential")
	}
	if reg.IsModelSuspendedForClient(id, model) || reg.GetModelCount(model) != 1 {
		t.Fatal("model directory retained a suspension after the scheduler recovered")
	}
	found := false
	for _, info := range reg.GetAvailableModels("openai") {
		found = found || info["id"] == model
	}
	if !found {
		t.Fatal("expired transient cooldown removed the model from discovery")
	}
}

func TestManager_CredentialQuotaKeepsSiblingModelsDiscoverable(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	id, model, sibling := t.Name(), t.Name()+"-model", t.Name()+"-sibling"
	if _, err := m.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive}); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model, Object: "model"}, {ID: sibling, Object: "model"}})
	t.Cleanup(func() { reg.UnregisterClient(id) })
	hint := time.Hour
	m.MarkResult(ctx, Result{AuthID: id, Provider: "claude", Model: model, Error: &Error{HTTPStatus: 429, Message: "quota exhausted"}, RetryAfter: &hint, CredentialScope: true})
	visible := make(map[string]bool)
	for _, info := range reg.GetAvailableModels("openai") {
		if name, ok := info["id"].(string); ok {
			visible[name] = true
		}
	}
	current, _ := m.GetByID(id)
	for _, name := range []string{model, sibling} {
		if !visible[name] {
			t.Fatalf("quota-limited model disappeared from discovery: %s", name)
		}
		if blocked, _, _ := isAuthBlockedForModel(current, name, time.Now()); !blocked || reg.GetModelCount(name) != 0 {
			t.Fatalf("discovery must not make a quota-limited model selectable: %s", name)
		}
	}
}

func TestManager_ModelProjectionKeepsExplicitDisable(t *testing.T) {
	now := time.Now()
	a := &Auth{Status: StatusActive, ModelStates: map[string]*ModelState{"model": {
		Status: StatusDisabled, Unavailable: true, NextRetryAfter: now.Add(-time.Hour),
		Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(-time.Hour)},
	}}}
	m := NewManager(nil, nil, nil)
	projection := m.clientModelProjectionForAuth(a, "model", now)
	if !projection.Suspended || !projection.SuspendUntil.IsZero() || projection.SuspendReason != "disabled" {
		t.Fatal("expiry changed an explicit model disable into a temporary suspension")
	}
}
