package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type lifecycleResultHook struct {
	NoopHook
	results []Result
}

func (h *lifecycleResultHook) OnResult(_ context.Context, result Result) {
	h.results = append(h.results, result)
}

type lifecycleBlockedExecutor struct {
	noForkAliasTestExecutor
	entered chan struct{}
	release chan struct{}
}

func (e *lifecycleBlockedExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	close(e.entered)
	<-e.release
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: 429, Message: "quota exceeded"}
}

func TestLifecycle_InflightResultCannotAffectReplacement(t *testing.T) {
	ctx := context.Background()
	hook := &lifecycleResultHook{}
	m := NewManager(nil, nil, hook)
	exec := &lifecycleBlockedExecutor{noForkAliasTestExecutor: noForkAliasTestExecutor{id: "claude"}, entered: make(chan struct{}), release: make(chan struct{})}
	m.RegisterExecutor(exec)
	id, model := t.Name(), "claude-test"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(id) })
	old, err := m.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, errExecute := m.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		done <- errExecute
	}()
	<-exec.entered
	m.Remove(ctx, id)
	reg.UnregisterClient(id)
	reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
	fresh, err := m.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	close(exec.release)
	if errExecute := <-done; errExecute == nil {
		t.Fatal("expected the original request failure")
	}
	current, _ := m.GetByID(id)
	if current.RegistrationEpoch != fresh.RegistrationEpoch || current.Unavailable || len(current.ModelStates) != 0 || current.Failed != 0 {
		t.Fatalf("old request mutated replacement: %#v", current)
	}
	if len(hook.results) != 1 || hook.results[0].RegistrationEpoch != old.RegistrationEpoch {
		t.Fatalf("original outcome was not reported with its registration: %#v", hook.results)
	}
	if reg.IsModelSuspendedForClient(id, model) {
		t.Fatal("old request suspended replacement model")
	}
}

func TestLifecycle_OldRegistryPublicationCannotRelabelReplacement(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	id, model := t.Name(), "claude-test"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(id) })
	old, _ := m.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive})
	m.Remove(ctx, id)
	reg.UnregisterClient(id)
	reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
	_, err := m.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	old.Generation = 100000
	old.ModelStates = map[string]*ModelState{model: {Unavailable: true, Status: StatusError, NextRetryAfter: time.Now().Add(time.Hour)}}
	m.publishAuthModelStates(old)
	if reg.IsModelSuspendedForClient(id, model) {
		t.Fatal("stale auth snapshot used the new registry epoch")
	}
}

func TestLifecycle_ManagerOwnsVersions(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, err := m.Register(context.Background(), &Auth{ID: t.Name(), Provider: "claude", RegistrationEpoch: ^uint64(0), Generation: ^uint64(0)})
	if err != nil || a.RegistrationEpoch != 1 || a.Generation != 1 {
		t.Fatalf("submitted versions influenced registration: %v, %v", a, err)
	}
	update := a.Clone()
	update.Generation = ^uint64(0)
	b, err := m.Update(context.Background(), update)
	if err != nil || b.Generation != 2 {
		t.Fatalf("submitted generation influenced update: %v, %v", b, err)
	}
	update = b.Clone()
	update.RegistrationEpoch++
	if _, errUpdate := m.Update(context.Background(), update); errUpdate == nil {
		t.Fatal("accepted a caller-assigned future registration")
	}
}

func TestLifecycle_OldAffinityResultCannotUnbindReplacement(t *testing.T) {
	ctx := context.Background()
	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	t.Cleanup(affinity.Stop)
	m := NewManager(nil, affinity, nil)
	id := t.Name()
	old, err := m.Register(ctx, &Auth{ID: id, Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	m.Remove(ctx, id)
	if _, errRegister := m.Register(ctx, &Auth{ID: id, Provider: "claude"}); errRegister != nil {
		t.Fatal(errRegister)
	}
	key := "claude::replacement-session::claude-test"
	affinity.cache.Set(key, id)
	m.updateSessionAffinity(bindResultAuth(Result{
		AuthID: id, Provider: "claude", Model: "claude-test",
		Error:   &Error{HTTPStatus: http.StatusTooManyRequests},
		Options: cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": {"replacement-session"}}},
	}, old))
	if got, ok := affinity.cache.Get(key); !ok || got != id {
		t.Fatal("old result removed replacement affinity")
	}
}

func TestLifecycle_InflightFailureCannotUndoDisableEnableReset(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	selected, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{true, false} {
		_, errPatch := m.PatchAuth(ctx, selected.ID, selected.RegistrationEpoch, func(a *Auth) error {
			a.Disabled = disabled
			if disabled {
				a.Status = StatusDisabled
			} else {
				a.Status = StatusActive
			}
			return nil
		})
		if errPatch != nil {
			t.Fatal(errPatch)
		}
	}
	m.MarkResult(ctx, bindResultAuth(Result{AuthID: selected.ID, Model: "claude-test", Error: &Error{HTTPStatus: 429}}, selected))
	current, _ := m.GetByID(selected.ID)
	if current.Unavailable || current.Quota.Exceeded || len(current.ModelStates) > 0 {
		t.Fatal("old failure restored state cleared by explicit disable/re-enable")
	}
	if current.Failed != 1 {
		t.Fatal("old outcome was not counted")
	}
}

func TestLifecycle_DelayedRemovalDoesNotDeleteReplacementStorage(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	old, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	m.Remove(ctx, old.ID)
	fresh, err := m.Register(ctx, &Auth{ID: old.ID, Provider: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if errRemove := m.RemoveWithPersistence(ctx, old.ID, func() error { called = true; return nil }, old.RegistrationEpoch); errRemove != nil {
		t.Fatal(errRemove)
	}
	current, exists := m.GetByID(old.ID)
	if called || !exists || current.RegistrationEpoch != fresh.RegistrationEpoch {
		t.Fatal("stale removal affected replacement")
	}
}
