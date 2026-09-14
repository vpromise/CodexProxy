package auth

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestLifecycle_CredentialRecoveryPreservesIndependentCooldowns(t *testing.T) {
	for _, mode := range []string{"patch", "file-replacement"} {
		for _, scenario := range []string{"only-401", "model-quota", "credential-quota", "auth-quota", "longer-capacity", "earlier-capacity", "auth-earlier-capacity", "newer-capacity", "other-model", "message-mentions-401"} {
			t.Run(mode+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				m := NewManager(nil, nil, nil)
				id, model := t.Name(), "claude-test"
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}, {ID: "sibling"}})
				t.Cleanup(func() { reg.UnregisterClient(id) })
				base, err := m.Register(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "old-token"}})
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "auth-quota" || scenario == "auth-earlier-capacity" {
					model = ""
				}
				fail := func(target string, status int, delay time.Duration, credentialScope bool) {
					m.MarkResult(ctx, bindResultAuth(Result{AuthID: id, Provider: "claude", Model: target,
						Error: &Error{HTTPStatus: status, Message: "upstream failure"}, RetryAfter: &delay, CredentialScope: credentialScope}, base))
				}
				if scenario == "model-quota" || scenario == "credential-quota" || scenario == "auth-quota" {
					fail(model, http.StatusTooManyRequests, 10*time.Minute, scenario == "credential-quota")
				}
				if scenario == "earlier-capacity" || scenario == "auth-earlier-capacity" {
					fail(model, 529, 10*time.Minute, false)
				}
				if scenario == "longer-capacity" {
					fail(model, 529, 2*time.Hour, false)
				}
				fail(model, http.StatusUnauthorized, 0, false)
				switch scenario {
				case "newer-capacity":
					fail(model, 529, time.Hour, false)
				case "other-model":
					fail("sibling", http.StatusServiceUnavailable, time.Hour, false)
				case "message-mentions-401":
					m.MarkResult(ctx, bindResultAuth(Result{AuthID: id, Provider: "claude", Model: model, Error: &Error{HTTPStatus: 503, Message: "backend saw 401 unauthorized previously"}}, base))
				}
				before, _ := m.GetByID(id)
				var after *Auth
				if mode == "patch" {
					after, err = m.PatchAuth(ctx, id, base.RegistrationEpoch, func(a *Auth) error { a.Metadata["access_token"] = "new-token"; return nil })
				} else {
					after, err = m.Update(ctx, &Auth{ID: id, Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "new-token"}})
				}
				if err != nil || after == nil {
					t.Fatalf("credential update: %v", err)
				}
				if authAccessToken(after) != "new-token" {
					t.Fatal("replacement token lost")
				}
				if scenario == "auth-earlier-capacity" {
					if !after.Unavailable || !after.NextRetryAfter.Equal(before.NonUnauthorizedRetryAfter) || after.LastError != nil {
						t.Fatal("401 recovery cleared earlier auth capacity block")
					}
					return
				}
				if scenario == "auth-quota" {
					if !after.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) || !after.NextRetryAfter.Equal(before.Quota.NextRecoverAt) || after.LastError != nil {
						t.Fatalf("auth quota changed: before=%+v after=%+v", before, after)
					}
					return
				}
				state := after.ModelStates[model]
				switch scenario {
				case "model-quota", "credential-quota":
					oldQuota := before.ModelStates[model].Quota
					if !reflect.DeepEqual(state.Quota, oldQuota) || !state.NextRetryAfter.Equal(oldQuota.NextRecoverAt) || state.LastError != nil || !state.Unavailable {
						t.Fatalf("401 recovery changed quota: %+v", state)
					}
					if scenario == "credential-quota" && (after.Quota.Reason != "credential_quota" || !after.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt)) {
						t.Fatalf("credential scope lost: %+v", after.Quota)
					}
				case "earlier-capacity":
					if state.LastError != nil || !state.Unavailable || !state.NextRetryAfter.Equal(before.ModelStates[model].NonUnauthorizedRetryAfter) {
						t.Fatalf("earlier capacity deadline changed: %+v", state)
					}
				case "longer-capacity":
					if state.LastError != nil || !state.Unavailable || !state.NextRetryAfter.Equal(before.ModelStates[model].NextRetryAfter) {
						t.Fatalf("independent capacity deadline changed: %+v", state)
					}
				case "newer-capacity", "message-mentions-401":
					if !reflect.DeepEqual(state, before.ModelStates[model]) {
						t.Fatalf("newer non-401 error was changed: %+v", state)
					}
				default:
					if state.LastError != nil || state.Unavailable || !state.NextRetryAfter.IsZero() || reg.IsModelSuspendedForClient(id, model) {
						t.Fatalf("obsolete 401 still blocks: %+v", state)
					}
					if scenario == "other-model" && !reflect.DeepEqual(after.ModelStates["sibling"], before.ModelStates["sibling"]) {
						t.Fatal("other model's cooldown changed")
					}
					if scenario == "other-model" && after.Unavailable {
						t.Fatal("recovered model did not clear aggregate unavailability")
					}
				}
			})
		}
	}
}

func TestLifecycle_ConfigEditDoesNotClearUnauthorized(t *testing.T) {
	ctx := context.Background()
	m := NewManager(nil, nil, nil)
	a, _ := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "same-token"}})
	m.MarkResult(ctx, bindResultAuth(Result{AuthID: a.ID, Model: "claude-test", Error: &Error{HTTPStatus: 401}}, a))
	before, _ := m.GetByID(a.ID)
	after, err := m.Update(ctx, &Auth{ID: a.ID, Provider: "claude", Status: StatusActive, ProxyURL: "http://proxy.invalid", Metadata: map[string]any{"access_token": "same-token", "notes": "updated"}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.ModelStates, before.ModelStates) {
		t.Fatal("configuration edit cleared unauthorized state")
	}
}

func TestLifecycle_UnauthorizedCleanupRetainsDisabledState(t *testing.T) {
	now := time.Now()
	for _, disabledAuth := range []bool{false, true} {
		a := &Auth{Disabled: disabledAuth, Status: StatusActive, ModelStates: map[string]*ModelState{"model": {Status: StatusDisabled, LastError: &Error{HTTPStatus: 401}, Unavailable: true, NextRetryAfter: now.Add(time.Hour)}}}
		before := a.Clone()
		if changed := ClearUnauthorizedModelStates(a, now); len(changed) != 0 || !reflect.DeepEqual(a, before) {
			t.Fatal("401 cleanup changed disabled state")
		}
	}
}

func TestLifecycle_IndependentCooldownSurvivesRestartAndExplicitReset(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprint(reset), func(t *testing.T) {
			ctx := context.Background()
			store := NewFileCooldownStateStore(t.TempDir())
			m := NewManager(nil, nil, nil)
			m.SetCooldownStateStore(store)
			a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "old"}})
			if err != nil {
				t.Fatal(err)
			}
			for _, status := range []int{529, 401} {
				delay := 10 * time.Minute
				m.MarkResult(ctx, bindResultAuth(Result{AuthID: a.ID, Model: "model", Error: &Error{HTTPStatus: status}, RetryAfter: &delay}, a))
			}
			before, _ := m.GetByID(a.ID)
			reloaded := NewManager(nil, nil, nil)
			if _, errRegister := reloaded.Register(ctx, a); errRegister != nil {
				t.Fatal(errRegister)
			}
			reloaded.SetCooldownStateStore(store)
			if errRestore := reloaded.RestoreCooldownStates(ctx); errRestore != nil {
				t.Fatal(errRestore)
			}
			if reset {
				if _, _, errReset := reloaded.ResetQuota(ctx, a.ID); errReset != nil {
					t.Fatal(errReset)
				}
				reloaded.MarkResult(ctx, Result{AuthID: a.ID, Model: "model", Error: &Error{HTTPStatus: 401}})
			}
			current, _ := reloaded.GetByID(a.ID)
			updated, errUpdate := reloaded.PatchAuth(ctx, a.ID, current.RegistrationEpoch, func(a *Auth) error { a.Metadata["access_token"] = "new"; return nil })
			if errUpdate != nil {
				t.Fatal(errUpdate)
			}
			state := updated.ModelStates["model"]
			if reset {
				if state.Unavailable || !state.NextRetryAfter.IsZero() {
					t.Fatal("explicit reset restored an old capacity cooldown")
				}
			} else if !state.Unavailable || !state.NextRetryAfter.Equal(before.ModelStates["model"].NonUnauthorizedRetryAfter) {
				t.Fatal("restart lost independent capacity cooldown")
			}
		})
	}
}
