package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestCredentialCooldownIsolationPreservesModelSelection(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, sharedModel := range []string{"fable", "sonnet"} {
		t.Run(sharedModel, func(t *testing.T) {
			manager, auth := newCooldownMonotonicManager(t, "fable", "sonnet", "opus")
			ctx := context.Background()
			for _, model := range []string{"fable", "sonnet", "opus"} {
				manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
			}
			modelRetry := 8 * 24 * time.Hour
			sharedRetry := 3 * time.Hour
			manager.MarkResult(ctx, Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "fable", RetryAfter: &modelRetry,
				Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "model quota exceeded"},
			})
			manager.MarkResult(ctx, Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: sharedModel, RetryAfter: &sharedRetry,
				CredentialScope: true, Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "shared quota exceeded"},
			})
			snapshot, _ := manager.GetByID(auth.ID)
			checkSelection := func(t *testing.T, wantHealthySiblings bool) {
				t.Helper()
				for _, model := range []string{"fable", "sonnet", "opus"} {
					for name, selector := range map[string]Selector{
						"round-robin": &RoundRobinSelector{},
						"fill-first":  &FillFirstSelector{},
						"weighted":    &WeightedRoundRobinSelector{},
					} {
						t.Run(model+"/"+name, func(t *testing.T) {
							picked, errPick := selector.Pick(ctx, auth.Provider, model, cliproxyexecutor.Options{}, []*Auth{snapshot})
							wantAvailable := wantHealthySiblings && model != "fable"
							if wantAvailable {
								if errPick != nil || picked == nil || picked.ID != auth.ID {
									t.Fatalf("healthy sibling unavailable: auth=%v error=%v", picked, errPick)
								}
							} else if !IsQuotaCooldownError(errPick) {
								t.Fatalf("expected quota cooldown, got auth=%v error=%v", picked, errPick)
							}
						})
					}
					t.Run(model+"/scheduler", func(t *testing.T) {
						scheduler := newAuthScheduler(&RoundRobinSelector{})
						scheduler.rebuild([]*Auth{snapshot})
						picked, errPick := scheduler.pickSingle(ctx, auth.Provider, model, cliproxyexecutor.Options{}, nil)
						if wantHealthySiblings && model != "fable" {
							if errPick != nil || picked == nil || picked.ID != auth.ID {
								t.Fatalf("scheduler rejected healthy sibling: auth=%v error=%v", picked, errPick)
							}
						} else if !IsQuotaCooldownError(errPick) {
							t.Fatalf("expected scheduler quota cooldown, got auth=%v error=%v", picked, errPick)
						}
					})
				}
			}
			t.Run("before_shared_reset", func(t *testing.T) { checkSelection(t, false) })

			// Shift the recorded deadlines to model elapsed time without sleeping or
			// independently clearing any cooldown that the manager recorded.
			elapsed := sharedRetry + time.Minute
			shift := func(value time.Time) time.Time {
				if value.IsZero() {
					return value
				}
				return value.Add(-elapsed)
			}
			snapshot.NextRetryAfter = shift(snapshot.NextRetryAfter)
			snapshot.Quota.NextRecoverAt = shift(snapshot.Quota.NextRecoverAt)
			for _, state := range snapshot.ModelStates {
				state.NextRetryAfter = shift(state.NextRetryAfter)
				state.NonUnauthorizedRetryAfter = shift(state.NonUnauthorizedRetryAfter)
				state.Quota.NextRecoverAt = shift(state.Quota.NextRecoverAt)
			}
			t.Run("after_shared_reset", func(t *testing.T) { checkSelection(t, true) })
		})
	}
}
