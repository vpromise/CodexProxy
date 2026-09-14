package auth

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestQuotaCooldownCauseAcrossSelectionPaths(t *testing.T) {
	const model = "quota-cause-model"
	now := time.Now()
	reset := now.Add(time.Hour)
	quota := &Auth{ID: "quota", Provider: "claude", ModelStates: map[string]*ModelState{
		model: {Unavailable: true, NextRetryAfter: reset, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: reset}},
	}}
	overload := &Auth{ID: "overload", Provider: "claude", ModelStates: map[string]*ModelState{
		model: {Unavailable: true, NextRetryAfter: reset, LastError: &Error{HTTPStatus: 529}},
	}}
	credential := &Auth{ID: "credential", Provider: "claude", Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: reset}, ModelStates: map[string]*ModelState{"other": {Status: StatusActive}}}
	challenge := quota.Clone()
	challenge.ID = "challenge"
	challenge.ModelStates[model].Quota.Reason = "cloudflare challenge"
	expired := overload.Clone()
	expired.ModelStates[model].Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(-time.Minute)}
	shortQuota := quota.Clone()
	shortQuota.ModelStates[model].NextRetryAfter = reset.Add(time.Hour)
	legacy := quota.Clone()
	legacy.ModelStates[model].Quota.Reason = ""
	legacy.ModelStates[model].LastError = &Error{HTTPStatus: http.StatusTooManyRequests}
	unknown := legacy.Clone()
	unknown.ModelStates[model].LastError = nil
	for _, tc := range []struct {
		name  string
		auths []*Auth
		quota bool
	}{
		{"model quota", []*Auth{quota}, true},
		{"credential quota", []*Auth{credential}, true},
		{"all quota", []*Auth{quota, credential}, true},
		{"overload", []*Auth{overload}, false},
		{"mixed causes", []*Auth{quota, overload}, false},
		{"challenge", []*Auth{challenge}, false},
		{"expired quota", []*Auth{expired}, false},
		{"later transient deadline", []*Auth{shortQuota}, false},
		{"legacy rate limit", []*Auth{legacy}, true},
		{"unknown cause", []*Auth{unknown}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"claude": {{Name: model, Alias: "quota-route", Fork: true}}})
			scheduler := newAuthScheduler(&RoundRobinSelector{})
			for _, candidate := range tc.auths {
				registry.GetGlobalRegistry().RegisterClient(candidate.ID, candidate.Provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(candidate.ID) })
			}
			scheduler.rebuild(tc.auths)
			checks := map[string]func() error{
				"round-robin": func() error {
					_, err := (&RoundRobinSelector{}).Pick(context.Background(), "claude", model+"(high)", cliproxyexecutor.Options{}, tc.auths)
					return err
				},
				"fill-first": func() error {
					_, err := (&FillFirstSelector{}).Pick(context.Background(), "claude", model, cliproxyexecutor.Options{}, tc.auths)
					return err
				},
				"weighted": func() error {
					_, err := (&WeightedRoundRobinSelector{}).Pick(context.Background(), "claude", model, cliproxyexecutor.Options{}, tc.auths)
					return err
				},
				"route alias": func() error {
					_, err := manager.availableAuthsForRouteModel(tc.auths, "claude", "quota-route", now)
					return err
				},
				"scheduler": func() error {
					_, err := scheduler.pickSingle(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil)
					return err
				},
				"mixed scheduler": func() error {
					_, _, err := scheduler.pickMixed(context.Background(), []string{"claude", "codex"}, model, cliproxyexecutor.Options{}, nil)
					return err
				},
			}
			for name, check := range checks {
				t.Run(name, func(t *testing.T) {
					errPick := check()
					if !IsModelCooldownError(errPick) {
						t.Fatalf("expected model cooldown, got %v", errPick)
					}
					if got := IsQuotaCooldownError(fmt.Errorf("selection: %w", errPick)); got != tc.quota {
						t.Fatalf("quota cause = %t, want %t", got, tc.quota)
					}
				})
			}
		})
	}
}

func TestQuotaCooldownCauseSurvivesModelRestoration(t *testing.T) {
	err := newModelCooldownError("", "claude", time.Hour)
	err.quotaLimited = true
	restored := restoreModelCooldownErrorModel(err, "alias(high)")
	if !IsQuotaCooldownError(restored) || err.model != "" {
		t.Fatal("model restoration lost the quota cause or mutated the original error")
	}
}

func TestQuotaCooldownCauseFollowsRecordedFailures(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, status := range []int{http.StatusTooManyRequests, 529, 520} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			manager, auth, model := newClaudeQuotaWarningManager(t)
			manager.RegisterExecutor(schedulerProviderTestExecutor{provider: "claude"})
			retryAfter := time.Hour
			manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "claude", Model: model, Error: &Error{HTTPStatus: status}, RetryAfter: &retryAfter})
			_, errSelect := manager.SelectAuth(context.Background(), "claude", model, cliproxyexecutor.Options{})
			if !IsModelCooldownError(errSelect) || IsQuotaCooldownError(errSelect) != (status == http.StatusTooManyRequests) {
				t.Fatalf("recorded HTTP %d was misclassified: %v", status, errSelect)
			}
		})
	}
}
