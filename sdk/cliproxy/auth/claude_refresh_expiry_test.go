package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type claudeExpiryExecutor struct {
	schedulerProviderTestExecutor
	refresh func(*Auth) error
	execute func(*Auth) error
}

func (e *claudeExpiryExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	return nil, e.refresh(a)
}

func (e *claudeExpiryExecutor) Execute(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.execute(a)
}

func (e *claudeExpiryExecutor) CountTokens(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.execute(a)
}

func claudeExpiryFixture(id string, expiry time.Time) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":             "fixture-access",
			"refresh_token":            "fixture-refresh",
			"expired":                  expiry.Format(time.RFC3339Nano),
			"refresh_interval_seconds": 60,
		},
	}
}

func TestManagerClaudeExpiredRefreshStopsWorkerAndRetainsValidAccess(t *testing.T) {
	ctx := t.Context()
	manager := NewManager(nil, nil, nil)
	calls := 0
	manager.RegisterExecutor(&claudeExpiryExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "claude"},
		refresh: func(*Auth) error {
			calls++
			return fmt.Errorf("token refresh failed: %w", &claudeauth.RefreshTokenExpiredError{})
		},
	})
	expiry := time.Now().Add(time.Hour)
	fixture := claudeExpiryFixture(t.Name(), expiry)
	if _, errRegister := manager.Register(ctx, fixture); errRegister != nil {
		t.Fatal(errRegister)
	}

	manager.refreshAuth(ctx, fixture.ID)
	current, _ := manager.GetByID(fixture.ID)
	now := time.Now()
	if !current.RequiresSignIn() || !current.NextRefreshAfter.IsZero() || manager.shouldRefresh(current, now) {
		t.Fatalf("confirmed expiry still scheduled or unrecorded: sign-in=%t next=%v", current.RequiresSignIn(), current.NextRefreshAfter)
	}
	if _, scheduled := nextRefreshCheckAt(now, current, time.Second); scheduled || manager.markRefreshPending(fixture.ID, now) {
		t.Fatal("confirmed expiry still enters the worker queue")
	}
	if blocked, _, _ := isAuthBlockedForModel(current, "claude-test", now); blocked {
		t.Fatal("confirmed expired refresh token blocked otherwise valid access")
	}
	if blocked, _, _ := isAuthBlockedForModel(current, "claude-test", expiry.Add(time.Second)); !blocked {
		t.Fatal("access remained eligible after it expired")
	}

	manager.refreshAuth(ctx, fixture.ID)
	if _, refreshed := manager.tryRefreshAfterUnauthorized(ctx, current, &Error{HTTPStatus: http.StatusUnauthorized}, false); refreshed || calls != 1 {
		t.Fatalf("confirmed expiry attempted another exchange, calls=%d, refreshed=%v", calls, refreshed)
	}

	persisted, errMarshal := json.Marshal(current)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	var restored Auth
	if errUnmarshal := json.Unmarshal(persisted, &restored); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if restored.RequiresSignIn() {
		t.Fatal("runtime sign-in state survived serialization")
	}
}

func TestManagerClaudeExpiredRefreshRequiresExactTypedClaudeOAuthFailure(t *testing.T) {
	for _, tc := range []struct {
		name, provider, kind string
		refreshErr           error
	}{
		{name: "raw text", provider: "claude", kind: AuthKindOAuth, refreshErr: &Error{HTTPStatus: http.StatusBadRequest, Message: "invalid_grant: Refresh token expired"}},
		{name: "codex", provider: "codex", kind: AuthKindOAuth, refreshErr: &claudeauth.RefreshTokenExpiredError{}},
		{name: "api key", provider: "claude", kind: AuthKindAPIKey, refreshErr: &claudeauth.RefreshTokenExpiredError{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.RegisterExecutor(&claudeExpiryExecutor{
				schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: tc.provider},
				refresh:                       func(*Auth) error { return tc.refreshErr },
			})
			fixture := claudeExpiryFixture(t.Name(), time.Now().Add(-time.Hour))
			fixture.Provider = tc.provider
			fixture.Attributes = map[string]string{AttributeAuthKind: tc.kind}
			if _, errRegister := manager.Register(t.Context(), fixture); errRegister != nil {
				t.Fatal(errRegister)
			}
			manager.refreshAuth(t.Context(), fixture.ID)
			current, _ := manager.GetByID(fixture.ID)
			if current.RequiresSignIn() || current.NextRefreshAfter.IsZero() {
				t.Fatal("refresh classified as terminal outside the exact native Claude OAuth case")
			}
		})
	}
}

func TestManagerClaudeExpiredRefreshHandlesVerified401AndFreshLogin(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	calls := 0
	manager.RegisterExecutor(&claudeExpiryExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "claude"},
		refresh: func(*Auth) error {
			calls++
			return &claudeauth.RefreshTokenExpiredError{}
		},
		execute: func(a *Auth) error {
			if authAccessToken(a) == "fixture-access" {
				return claudeauth.MarkAccessTokenRejection(&Error{HTTPStatus: http.StatusUnauthorized, Message: "fixture rejection"})
			}
			return nil
		},
	})
	fixture := claudeExpiryFixture(t.Name(), time.Now().Add(time.Hour))
	const first, second = "claude-test-one", "claude-test-two"
	models := registry.GetGlobalRegistry()
	models.RegisterClient(fixture.ID, "claude", []*registry.ModelInfo{{ID: first}, {ID: second}})
	t.Cleanup(func() { models.UnregisterClient(fixture.ID) })
	current, errRegister := manager.Register(t.Context(), fixture)
	if errRegister != nil {
		t.Fatal(errRegister)
	}

	// A verified 401 and the refresh rejection can first arrive in the same request.
	_, errExecute := manager.Execute(t.Context(), []string{"claude"}, cliproxyexecutor.Request{Model: first}, cliproxyexecutor.Options{})
	if errExecute == nil || calls != 1 {
		t.Fatalf("calls=%d err=%v, want one terminal exchange", calls, errExecute)
	}
	afterFailure, _ := manager.GetByID(fixture.ID)
	if !afterFailure.SignInBlocksDispatch(time.Now()) {
		t.Fatal("rejected access still eligible before its nominal expiry")
	}
	if _, errSelect := manager.SelectAuth(t.Context(), "claude", second, cliproxyexecutor.Options{}); errSelect == nil {
		t.Fatal("credential-wide rejection did not remove this auth from a sibling model")
	}

	afterEdit, errEdit := manager.PatchAuth(t.Context(), fixture.ID, current.RegistrationEpoch, func(a *Auth) error {
		a.Metadata["note"] = "operator note"
		return nil
	})
	if errEdit != nil || !afterEdit.SignInBlocksDispatch(time.Now()) {
		t.Fatalf("unrelated edit cleared rejection state: %v", errEdit)
	}
	afterLogin, errLogin := manager.PatchAuth(t.Context(), fixture.ID, current.RegistrationEpoch, func(a *Auth) error {
		a.Metadata["access_token"] = "fixture-new-access"
		a.Metadata["refresh_token"] = "fixture-new-refresh"
		return nil
	})
	if errLogin != nil || afterLogin.RequiresSignIn() || afterLogin.CredentialVersion <= current.CredentialVersion {
		t.Fatalf("fresh login retained the old sign-in state: %v", errLogin)
	}
	late := Result{AuthID: fixture.ID, Model: second, Error: &Error{HTTPStatus: http.StatusUnauthorized}, claudeAccessRejected: true}
	manager.MarkResult(t.Context(), bindResultAuth(late, afterFailure))
	if _, errSelect := manager.SelectAuth(t.Context(), "claude", second, cliproxyexecutor.Options{}); errSelect != nil {
		t.Fatalf("stale 401 blocked newly authorized credential: %v", errSelect)
	}
}

func TestManagerClaudeExpiredRefreshCount401DoesNotBlockSiblingModels(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	calls := 0
	manager.RegisterExecutor(&claudeExpiryExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "claude"},
		refresh: func(*Auth) error {
			calls++
			return &claudeauth.RefreshTokenExpiredError{}
		},
		execute: func(*Auth) error {
			// A custom executor cannot turn a count-only rejection into an account-wide latch.
			return claudeauth.MarkAccessTokenRejection(&Error{HTTPStatus: http.StatusUnauthorized, Message: "fixture token-count rejection"})
		},
	})
	fixture := claudeExpiryFixture(t.Name(), time.Now().Add(time.Hour))
	const counted, sibling = "claude-counted-model", "claude-sibling-model"
	models := registry.GetGlobalRegistry()
	models.RegisterClient(fixture.ID, "claude", []*registry.ModelInfo{{ID: counted}, {ID: sibling}})
	t.Cleanup(func() { models.UnregisterClient(fixture.ID) })
	if _, errRegister := manager.Register(t.Context(), fixture); errRegister != nil {
		t.Fatal(errRegister)
	}
	manager.refreshAuth(t.Context(), fixture.ID)
	_, errCount := manager.ExecuteCount(t.Context(), []string{"claude"}, cliproxyexecutor.Request{Model: counted}, cliproxyexecutor.Options{})
	current, _ := manager.GetByID(fixture.ID)
	if statusCodeFromError(errCount) != http.StatusUnauthorized || calls != 1 || !current.RequiresSignIn() || current.SignInBlocksDispatch(time.Now()) {
		t.Fatalf("token-count 401 changed account-wide availability or restarted refresh: calls=%d err=%v blocked=%t", calls, errCount, current.SignInBlocksDispatch(time.Now()))
	}
	if _, errSelect := manager.SelectAuth(t.Context(), "claude", sibling, cliproxyexecutor.Options{}); errSelect != nil {
		t.Fatalf("token-count 401 blocked sibling model: %v", errSelect)
	}
}

func TestManagerClaudeExpiredRefresh401RespectsManualDisableAndAvailabilityEpoch(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&claudeExpiryExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "claude"},
		refresh:                       func(*Auth) error { return &claudeauth.RefreshTokenExpiredError{} },
	})
	fixture := claudeExpiryFixture(t.Name(), time.Now().Add(time.Hour))
	if _, errRegister := manager.Register(t.Context(), fixture); errRegister != nil {
		t.Fatal(errRegister)
	}
	manager.refreshAuth(t.Context(), fixture.ID)
	beforeDisable, _ := manager.GetByID(fixture.ID)
	disabled, errDisable := manager.PatchAuth(t.Context(), fixture.ID, beforeDisable.RegistrationEpoch, func(a *Auth) error {
		a.Disabled = true
		a.Status = StatusDisabled
		return nil
	})
	if errDisable != nil {
		t.Fatal(errDisable)
	}
	rejected := Result{AuthID: fixture.ID, Model: "claude-test", Error: &Error{HTTPStatus: http.StatusUnauthorized}, claudeAccessRejected: true}
	manager.MarkResult(t.Context(), bindResultAuth(rejected, disabled))
	current, _ := manager.GetByID(fixture.ID)
	if current.SignInBlocksDispatch(time.Now()) || !current.Disabled {
		t.Fatal("a rejection changed the manually disabled auth's sign-in state")
	}
	if _, errEnable := manager.PatchAuth(t.Context(), fixture.ID, beforeDisable.RegistrationEpoch, func(a *Auth) error {
		a.Disabled = false
		a.Status = StatusActive
		return nil
	}); errEnable != nil {
		t.Fatal(errEnable)
	}
	manager.MarkResult(t.Context(), bindResultAuth(rejected, beforeDisable))
	current, _ = manager.GetByID(fixture.ID)
	if current.Disabled || !current.RequiresSignIn() || current.SignInBlocksDispatch(time.Now()) {
		t.Fatal("a rejection from before the operator reset changed availability")
	}
}

func TestManagerClaudeExpiredRefreshPreservesCooldownAndRejectsStaleResult(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	started, release := make(chan struct{}), make(chan struct{})
	call := 0
	manager.RegisterExecutor(&claudeExpiryExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "claude"},
		refresh: func(*Auth) error {
			call++
			if call == 1 {
				close(started)
				<-release
			}
			return &claudeauth.RefreshTokenExpiredError{}
		},
	})
	fixture := claudeExpiryFixture(t.Name(), time.Now().Add(time.Hour))
	deadline := time.Now().Add(30 * time.Minute)
	fixture.Status = StatusError
	fixture.StatusMessage = "quota_exceeded"
	fixture.Unavailable = true
	fixture.NextRetryAfter = deadline
	fixture.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: deadline}
	current, errRegister := manager.Register(t.Context(), fixture)
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		manager.refreshAuth(t.Context(), fixture.ID)
	}()
	<-started
	afterLogin, errLogin := manager.PatchAuth(t.Context(), fixture.ID, current.RegistrationEpoch, func(a *Auth) error {
		a.Metadata["refresh_token"] = "fixture-new-refresh"
		return nil
	})
	close(release)
	<-done
	if errLogin != nil || afterLogin == nil {
		t.Fatalf("fresh login failed: %v", errLogin)
	}
	afterOldResult, _ := manager.GetByID(fixture.ID)
	if afterOldResult.RequiresSignIn() {
		t.Fatal("late result marked the newer refresh token expired")
	}
	manager.refreshAuth(t.Context(), fixture.ID)
	afterCurrentResult, _ := manager.GetByID(fixture.ID)
	if !afterCurrentResult.RequiresSignIn() || afterCurrentResult.StatusMessage != "quota_exceeded" ||
		!afterCurrentResult.NextRetryAfter.Equal(deadline) || !afterCurrentResult.Quota.NextRecoverAt.Equal(deadline) {
		t.Fatalf("expiry overwrote preexisting quota state: sign-in=%t status=%s deadline=%v quota=%v",
			afterCurrentResult.RequiresSignIn(), afterCurrentResult.StatusMessage, afterCurrentResult.NextRetryAfter, afterCurrentResult.Quota.NextRecoverAt)
	}
}
