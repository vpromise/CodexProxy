package auth

import (
	"net/http"
	"testing"
	"time"
)

func TestAccountCooldownOrder(t *testing.T) {
	for _, tc := range []struct {
		name      string
		startedAt func(time.Time) time.Time
		wantClear bool
	}{
		{"older success", func(at time.Time) time.Time { return at.Add(-time.Minute) }, false},
		{"unstamped success", func(time.Time) time.Time { return time.Time{} }, false},
		{"simultaneous success", func(at time.Time) time.Time { return at }, false},
		{"newer success", func(time.Time) time.Time { return time.Now() }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			manager := NewManager(nil, nil, nil)
			account, err := manager.Register(ctx, &Auth{
				ID: "fixture", Provider: "claude", Metadata: map[string]any{"disable_cooling": false},
			})
			if err != nil {
				t.Fatal(err)
			}

			retry := 10 * time.Minute
			manager.MarkResult(ctx, bindResultAuth(Result{
				AuthID: account.ID, Provider: account.Provider, RetryAfter: &retry,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limit"},
			}, account))
			before, _ := manager.GetByID(account.ID)
			if !before.Unavailable || before.NextRetryAfter.IsZero() {
				t.Fatal("fixture did not establish an account cooldown")
			}

			started := tc.startedAt(before.UpdatedAt)
			// An unrelated observation advances UpdatedAt without establishing a cooldown.
			manager.MarkResult(ctx, bindResultAuth(Result{
				AuthID: account.ID, Provider: account.Provider, Success: true, SkipQuotaObservation: true,
			}, account))
			manager.MarkResult(ctx, bindResultAuth(Result{
				AuthID: account.ID, Provider: account.Provider, Success: true, AttemptStartedAt: started,
			}, account))

			after, _ := manager.GetByID(account.ID)
			if after.Success != before.Success+2 {
				t.Fatalf("success count = %d, want %d", after.Success, before.Success+2)
			}
			if tc.wantClear {
				if after.Unavailable || !after.NextRetryAfter.IsZero() {
					t.Fatalf("newer attempt could not recover the account, deadline=%v", after.NextRetryAfter)
				}
				return
			}
			if !after.Unavailable || !after.NextRetryAfter.Equal(before.NextRetryAfter) ||
				!after.Quota.NextRecoverAt.Equal(before.Quota.NextRecoverAt) {
				t.Fatalf("success cleared or changed the existing account cooldown, before=%v after=%v", before.NextRetryAfter, after.NextRetryAfter)
			}
		})
	}
}

func TestAccountCooldownOrderRestoredDeadline(t *testing.T) {
	ctx := t.Context()
	store := NewFileCooldownStateStore(t.TempDir())
	now := time.Now()
	record := CooldownStateRecord{
		AuthID: "fixture", Provider: "claude", Status: "cooling",
		NextRetryAfter: now.Add(10 * time.Minute), UpdatedAt: now.Add(-time.Minute),
	}
	if err := store.Save(ctx, []CooldownStateRecord{record}); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(nil, nil, nil)
	account, err := manager.Register(ctx, &Auth{
		ID: record.AuthID, Provider: record.Provider, Metadata: map[string]any{"disable_cooling": false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !manager.SwapCooldownStateStore(ctx, store, false) {
		t.Fatal("failed to install cooldown store")
	}
	if err := manager.RestoreCooldownStates(ctx); err != nil {
		t.Fatal(err)
	}
	before, _ := manager.GetByID(account.ID)
	if !before.Unavailable || !before.NextRetryAfter.Equal(record.NextRetryAfter) {
		t.Fatalf("fixture did not restore the deadline: %v", before.NextRetryAfter)
	}

	// The persisted UpdatedAt can reflect bookkeeping instead of the failure.
	manager.MarkResult(ctx, bindResultAuth(Result{
		AuthID: account.ID, Provider: account.Provider, Success: true, AttemptStartedAt: time.Now(),
	}, account))
	after, _ := manager.GetByID(account.ID)
	if !after.Unavailable || !after.NextRetryAfter.Equal(record.NextRetryAfter) {
		t.Fatalf("restored cooldown without an observation was cleared: %v", after.NextRetryAfter)
	}
	if blocked, _, _ := isAuthBlockedForModel(after, "", record.NextRetryAfter); blocked {
		t.Fatal("account did not naturally recover at the recorded deadline")
	}
}
