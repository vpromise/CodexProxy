package auth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCredentialVersionRejectsLateResults(t *testing.T) {
	for _, status := range []int{0, 401, 429} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			base, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "old"}})
			if err != nil {
				t.Fatal(err)
			}
			late := bindResultAuth(Result{AuthID: base.ID, Model: "test", Success: status == 0}, base)
			if status != 0 {
				late.Error = &Error{HTTPStatus: status, Message: "old failure"}
			}
			current, err := m.PatchAuth(t.Context(), base.ID, base.RegistrationEpoch, func(a *Auth) error { a.Metadata["access_token"] = "new"; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if status == 0 {
				m.MarkResult(t.Context(), bindResultAuth(Result{AuthID: base.ID, Model: "test", Error: &Error{HTTPStatus: 401, Message: "new failure"}}, current))
			}
			before, _ := m.GetByID(base.ID)
			m.MarkResult(t.Context(), late)
			after, _ := m.GetByID(base.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("stale credential result changed current state")
			}
		})
	}
}

func TestCredentialVersionRefreshABA(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			exec := &lifecycleRefreshExecutor{noForkAliasTestExecutor: noForkAliasTestExecutor{id: "claude"}, started: make(chan struct{}), release: make(chan struct{}), fail: failure}
			m.RegisterExecutor(exec)
			base, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "A", "refresh_token": "refresh-A"}})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() { _, _ = m.refreshAuthForRequest(t.Context(), base.ID, ""); close(done) }()
			<-exec.started
			for _, token := range []string{"B", "A"} {
				if _, err = m.PatchAuth(t.Context(), base.ID, base.RegistrationEpoch, func(a *Auth) error { a.Metadata["access_token"] = token; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := m.GetByID(base.ID)
			close(exec.release)
			<-done
			after, _ := m.GetByID(base.ID)
			if CredentialsChanged(before, after) || after.LastError != nil || !after.LastRefreshedAt.Equal(before.LastRefreshedAt) || !after.NextRefreshAfter.Equal(before.NextRefreshAfter) {
				t.Fatal("obsolete refresh changed credentials or lifecycle state after ABA edit")
			}
		})
	}
}

func TestCredentialVersionTracksOnlyCredentialMaterial(t *testing.T) {
	for _, field := range []string{"access_token", "refresh_token", "id_token", "api_key"} {
		t.Run(field, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			a, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "claude", CredentialVersion: ^uint64(0), Metadata: map[string]any{field: "old"}})
			if err != nil || a.CredentialVersion != 1 {
				t.Fatalf("initial version=%v error=%v", a, err)
			}
			edited := a.Clone()
			edited.Label = "renamed"
			edited.CredentialVersion = 999
			b, err := m.Update(t.Context(), edited)
			if err != nil || b.CredentialVersion != a.CredentialVersion || b.Generation <= a.Generation {
				t.Fatalf("config update changed credential version: %v, %v", b, err)
			}
			m.MarkResult(t.Context(), bindResultAuth(Result{AuthID: a.ID, Success: true}, a))
			before, _ := m.GetByID(a.ID)
			if before.Success != 1 || before.CredentialVersion != a.CredentialVersion {
				t.Fatal("config change discarded a current credential result")
			}
			changed, err := m.PatchAuth(t.Context(), a.ID, a.RegistrationEpoch, func(current *Auth) error {
				current.Metadata[field] = "new"
				current.CredentialVersion = 999
				return nil
			})
			if err != nil || changed.CredentialVersion != a.CredentialVersion+1 {
				t.Fatalf("token change did not advance managed version: %v, %v", changed, err)
			}
			blob, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]any
			if errDecode := json.Unmarshal(blob, &fields); errDecode != nil {
				t.Fatal(errDecode)
			}
			if _, exists := fields["credential_version"]; exists {
				t.Fatal("runtime credential version leaked into serialization")
			}
			if _, exists := fields["CredentialVersion"]; exists {
				t.Fatal("runtime credential version leaked into serialization")
			}
			m.MarkResult(t.Context(), Result{AuthID: a.ID, Success: true})
			legacy, _ := m.GetByID(a.ID)
			if legacy.Success != before.Success+1 {
				t.Fatal("legacy unversioned result stopped working")
			}
		})
	}
}

func TestCredentialVersionRejectsStalePreparedMetadata(t *testing.T) {
	m := NewManager(nil, nil, nil)
	a, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "claude", Metadata: map[string]any{"access_token": "A"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"B", "A"} {
		_, err = m.PatchAuth(t.Context(), a.ID, a.RegistrationEpoch, func(current *Auth) error { current.Metadata["access_token"] = token; return nil })
		if err != nil {
			t.Fatal(err)
		}
	}
	before, _ := m.GetByID(a.ID)
	prepared := a.Clone()
	prepared.Metadata["account_id"] = "obsolete-account"
	result, err := m.UpdatePreparedAuth(t.Context(), a, prepared)
	after, _ := m.GetByID(a.ID)
	if err != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(after, result) {
		t.Fatalf("stale prepare changed current state: %v", err)
	}
	refreshed := after.Clone()
	refreshed.Metadata["access_token"] = "refreshed"
	current, err := m.UpdateRefreshedAuth(t.Context(), after, refreshed)
	if err != nil || current.CredentialVersion != after.CredentialVersion+1 {
		t.Fatalf("current refresh did not advance version: %v", err)
	}
}

func TestCredentialVersionLateFailureCannotRemoveAffinity(t *testing.T) {
	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	t.Cleanup(affinity.Stop)
	m := NewManager(nil, affinity, nil)
	a, err := m.Register(t.Context(), &Auth{ID: t.Name(), Provider: "claude", Metadata: map[string]any{"access_token": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.PatchAuth(t.Context(), a.ID, a.RegistrationEpoch, func(current *Auth) error { current.Metadata["access_token"] = "new"; return nil })
	if err != nil {
		t.Fatal(err)
	}
	key := "claude::session::test"
	affinity.cache.Set(key, a.ID)
	m.updateSessionAffinity(bindResultAuth(Result{AuthID: a.ID, Provider: "claude", Model: "test", Error: &Error{HTTPStatus: 429}, Options: cliproxyexecutor.Options{Headers: http.Header{"X-Session-Id": {"session"}}}}, a))
	if got, ok := affinity.cache.Get(key); !ok || got != a.ID {
		t.Fatal("old token failure removed current session affinity")
	}
}

func TestCredentialVersionReloadStartsNewRegistration(t *testing.T) {
	store := newMemoryAuthTestStore()
	_, err := store.Save(t.Context(), &Auth{ID: t.Name(), Provider: "claude", CredentialVersion: 99, Metadata: map[string]any{"access_token": "current"}})
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(store, nil, nil)
	if err = m.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	old, _ := m.GetByID(t.Name())
	if old.CredentialVersion != 1 {
		t.Fatal("load trusted a persisted runtime version")
	}
	late := bindResultAuth(Result{AuthID: old.ID, Model: "test", Error: &Error{HTTPStatus: 401}}, old)
	if err = m.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	before, _ := m.GetByID(old.ID)
	m.MarkResult(t.Context(), late)
	after, _ := m.GetByID(old.ID)
	if after.CredentialVersion != 1 || after.RegistrationEpoch <= old.RegistrationEpoch || !reflect.DeepEqual(before, after) {
		t.Fatal("reload accepted a result from the old registration")
	}
}
