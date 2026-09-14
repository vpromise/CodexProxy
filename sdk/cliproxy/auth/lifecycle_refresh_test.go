package auth

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

type lifecycleRefreshExecutor struct {
	noForkAliasTestExecutor
	started chan struct{}
	release chan struct{}
	fail    bool
}

func (e *lifecycleRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	if e.fail {
		return nil, &Error{HTTPStatus: 401, Code: "unauthorized", Message: "refresh rejected"}
	}
	auth.Metadata["access_token"] = "refreshed-access"
	auth.Metadata["refresh_token"] = "refreshed-refresh"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	auth.Runtime = "refreshed-runtime"
	return auth, nil
}

func TestLifecycle_RefreshConcurrentChanges(t *testing.T) {
	for _, failure := range []bool{false, true} {
		for _, change := range []string{"disable", "disable-enable", "replace-token", "delete", "recreate", "extend-quota", "edit-config"} {
			t.Run(fmt.Sprintf("%s/failure=%v", change, failure), func(t *testing.T) {
				ctx := context.Background()
				store := newMemoryAuthTestStore()
				m := NewManager(store, nil, nil)
				exec := &lifecycleRefreshExecutor{noForkAliasTestExecutor: noForkAliasTestExecutor{id: "claude"}, started: make(chan struct{}), release: make(chan struct{}), fail: failure}
				m.RegisterExecutor(exec)
				a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive,
					Metadata: map[string]any{"access_token": "initial-access", "refresh_token": "initial-refresh", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)}, Runtime: "initial-runtime"})
				if err != nil {
					t.Fatal(err)
				}
				if change == "extend-quota" {
					retry := time.Minute
					m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "claude", Model: "test-model", Error: &Error{HTTPStatus: 429, Message: "same quota error"}, RetryAfter: &retry, CredentialScope: true})
				}
				done := make(chan struct{})
				go func() {
					_, _ = m.refreshAuthForRequest(ctx, a.ID, "")
					close(done)
				}()
				<-exec.started
				current, _ := m.GetByID(a.ID)
				switch change {
				case "disable":
					current.Disabled, current.Status = true, StatusDisabled
					current.Metadata["disabled"] = true
					_, err = m.Update(ctx, current)
				case "disable-enable":
					for _, disabled := range []bool{true, false} {
						_, err = m.PatchAuth(ctx, current.ID, current.RegistrationEpoch, func(a *Auth) error {
							a.Disabled = disabled
							if disabled {
								a.Status = StatusDisabled
							} else {
								a.Status = StatusActive
							}
							return nil
						})
						if err != nil {
							break
						}
					}
				case "replace-token":
					current.Metadata["access_token"] = "user-access"
					current.Metadata["refresh_token"] = "user-refresh"
					current.Runtime = "user-runtime"
					current.NextRefreshAfter = time.Now().Add(10 * time.Minute)
					_, err = m.Update(ctx, current)
				case "delete", "recreate":
					m.Remove(ctx, a.ID)
					err = store.Delete(ctx, a.ID)
					if change == "recreate" {
						current.RegistrationEpoch = 0
						current.Metadata["access_token"] = "replacement-access"
						current, err = m.Register(ctx, current)
					}
				case "extend-quota":
					retry := 2 * time.Hour
					m.MarkResult(ctx, Result{AuthID: a.ID, Provider: "claude", Model: "test-model", Error: &Error{HTTPStatus: 429, Message: "same quota error"}, RetryAfter: &retry, CredentialScope: true})
				case "edit-config":
					current.ProxyURL = "http://user-proxy.invalid"
					current.Prefix = "user-prefix"
					current.Metadata["notes"] = "user edit"
					_, err = m.Update(ctx, current)
				}
				if err != nil {
					close(exec.release)
					<-done
					t.Fatal(err)
				}
				before, _ := m.GetByID(a.ID)
				close(exec.release)
				<-done
				after, exists := m.GetByID(a.ID)
				if change == "delete" {
					if exists {
						t.Fatal("refresh resurrected deleted auth")
					}
					if errPersist := m.persist(ctx, a); errPersist != nil {
						t.Fatal(errPersist)
					}
					stored, _ := store.List(ctx)
					if len(stored) != 0 {
						t.Fatal("stale persistence resurrected deleted auth")
					}
					return
				}
				if !exists {
					t.Fatal("auth disappeared")
				}
				switch change {
				case "disable":
					if !after.Disabled || after.Status != StatusDisabled || after.Unavailable || !after.NextRetryAfter.IsZero() || after.LastError != nil {
						t.Fatalf("refresh changed disabled state: %#v", after)
					}
				case "disable-enable":
					if after.Disabled || after.Status != StatusActive || after.Unavailable || !after.NextRetryAfter.IsZero() || after.LastError != nil {
						t.Fatal("refresh restored state cleared by disable/re-enable")
					}
				case "replace-token", "recreate":
					if CredentialsChanged(before, after) || before.Runtime != after.Runtime || !after.NextRefreshAfter.Equal(before.NextRefreshAfter) || !after.LastRefreshedAt.Equal(before.LastRefreshedAt) || after.RegistrationEpoch != before.RegistrationEpoch || after.LastError != nil {
						t.Fatal("old refresh affected replacement credentials or lifecycle state")
					}
				case "extend-quota":
					if !reflect.DeepEqual(before.Quota, after.Quota) || !reflect.DeepEqual(before.ModelStates, after.ModelStates) || !reflect.DeepEqual(before.LastError, after.LastError) || !before.NextRetryAfter.Equal(after.NextRetryAfter) {
						t.Fatal("refresh changed the newer quota or failure")
					}
				case "edit-config":
					if after.ProxyURL != current.ProxyURL || after.Prefix != current.Prefix || after.Metadata["notes"] != "user edit" {
						t.Fatal("refresh overwrote user configuration")
					}
				}
				stored, _ := store.List(ctx)
				if len(stored) != 1 || stored[0].Disabled != after.Disabled || CredentialsChanged(stored[0], after) || stored[0].Runtime != after.Runtime || stored[0].ProxyURL != after.ProxyURL {
					t.Fatal("stored credential differs from the accepted runtime update")
				}
			})
		}
	}
}

func TestLifecycle_CloneIsolatesNestedMetadataAndErrors(t *testing.T) {
	base := &Auth{Metadata: map[string]any{"token": map[string]any{"access_token": "base"}, "list": []any{map[string]any{"value": "base"}}}, LastError: &Error{Message: "base"}}
	copyAuth := base.Clone()
	copyAuth.Metadata["token"].(map[string]any)["access_token"] = "changed"
	copyAuth.Metadata["list"].([]any)[0].(map[string]any)["value"] = "changed"
	copyAuth.LastError.Message = "changed"
	if base.Metadata["token"].(map[string]any)["access_token"] != "base" || base.Metadata["list"].([]any)[0].(map[string]any)["value"] != "base" || base.LastError.Message != "base" {
		t.Fatal("executor clone mutated the merge base")
	}
}

func TestLifecycle_PreparationCannotReturnDeletedOrDisabledAuth(t *testing.T) {
	for _, change := range []string{"delete", "disable", "recreate"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			m := NewManager(nil, nil, nil)
			exec := &testPrepareExecutor{schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "claude"}, started: make(chan struct{}), release: make(chan struct{})}
			a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "initial"}})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, errPrepare := m.prepareRequestAuth(ctx, exec, a); done <- errPrepare }()
			<-exec.started
			if change == "disable" {
				current := a.Clone()
				current.Disabled, current.Status = true, StatusDisabled
				_, err = m.Update(ctx, current)
			} else {
				m.Remove(ctx, a.ID)
				if change == "recreate" {
					_, err = m.Register(ctx, &Auth{ID: a.ID, Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "replacement"}})
				}
			}
			close(exec.release)
			errPrepare := <-done
			if err != nil || errPrepare == nil {
				t.Fatalf("mutation error=%v; preparation should fail, got %v", err, errPrepare)
			}
			if current, exists := m.GetByID(a.ID); exists {
				if change == "disable" {
					if !current.Disabled || current.Status != StatusDisabled {
						t.Fatal("preparation undid disable")
					}
				} else if current.Metadata["project_id"] != nil {
					t.Fatal("stale preparation metadata reached replacement credential")
				}
			}
		})
	}
}

type lifecycleFailOnceStore struct {
	*memoryAuthTestStore
	fail bool
}

func (s *lifecycleFailOnceStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if s.fail {
		s.fail = false
		return "", fmt.Errorf("injected save failure")
	}
	return s.memoryAuthTestStore.Save(ctx, auth)
}

func TestLifecycle_FailedPersistenceCanRetryCurrentGeneration(t *testing.T) {
	ctx := context.Background()
	store := &lifecycleFailOnceStore{memoryAuthTestStore: newMemoryAuthTestStore(), fail: true}
	m := NewManager(store, nil, nil)
	a, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Metadata: map[string]any{"access_token": "current"}})
	if err == nil || a == nil {
		t.Fatal("expected a reported save failure with retained runtime auth")
	}
	if errPersist := m.persist(ctx, a); errPersist != nil {
		t.Fatal(errPersist)
	}
	saved, _ := store.List(ctx)
	if len(saved) != 1 || authAccessToken(saved[0]) != "current" {
		t.Fatal("failed save prevented retry of the current generation")
	}
}

func TestLifecycle_ManagementPatchRetainsNewerToken(t *testing.T) {
	ctx := context.Background()
	m := NewManager(newMemoryAuthTestStore(), nil, nil)
	base, err := m.Register(ctx, &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "before", "notes": "before"}})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := base.Clone()
	refreshed.Metadata["access_token"] = "rotated"
	if _, errUpdate := m.UpdateRefreshedAuth(ctx, base, refreshed); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	patched, errPatch := m.PatchAuth(ctx, base.ID, base.RegistrationEpoch, func(latest *Auth) error {
		latest.Disabled, latest.Status = true, StatusDisabled
		latest.Metadata["disabled"] = true
		latest.Metadata["notes"] = "user edit"
		return nil
	})
	if errPatch != nil || !patched.Disabled || authAccessToken(patched) != "rotated" || patched.Metadata["notes"] != "user edit" {
		t.Fatalf("management patch rolled back refresh: %#v, %v", patched, errPatch)
	}
}
