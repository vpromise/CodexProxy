package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type claudeSignInFixtureExecutor struct {
	cliproxyauth.ProviderExecutor
	refreshCalls int
	messageCalls int
}

func (*claudeSignInFixtureExecutor) Identifier() string { return "claude" }

func (e *claudeSignInFixtureExecutor) Refresh(_ context.Context, _ *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	e.refreshCalls++
	return nil, &claude.RefreshTokenExpiredError{}
}

func (e *claudeSignInFixtureExecutor) Execute(context.Context, *cliproxyauth.Auth, executor.Request, executor.Options) (executor.Response, error) {
	e.messageCalls++
	return executor.Response{}, claude.MarkAccessTokenRejection(&cliproxyauth.Error{HTTPStatus: http.StatusUnauthorized})
}

func TestFileTokenStoreRestoresClaudeSignInAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const (
			name  = "claude-signin-restart.json"
			model = "claude-signin-restart-fixture"
		)
		dir := t.TempDir()
		path := filepath.Join(dir, name)
		input, errMarshal := json.Marshal(map[string]any{
			"type": "claude", "access_token": "fixture-old-access", "refresh_token": "fixture-old-refresh",
			"expired": time.Now().Add(time.Hour).Format(time.RFC3339), "refresh_interval_seconds": 60, "disabled": false,
		})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if errWrite := os.WriteFile(path, input, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		provider := &claudeSignInFixtureExecutor{}
		models := registry.GetGlobalRegistry()
		models.RegisterClient(name, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { models.UnregisterClient(name) })
		load := func() (*cliproxyauth.Manager, *cliproxyauth.Auth) {
			t.Helper()
			store := NewFileTokenStore()
			store.SetBaseDir(dir)
			manager := cliproxyauth.NewManager(store, nil, nil)
			manager.RegisterExecutor(provider)
			if errLoad := manager.Load(t.Context()); errLoad != nil {
				t.Fatal(errLoad)
			}
			auth, ok := manager.GetByID(name)
			if !ok {
				t.Fatal("Claude credential is missing")
			}
			return manager, auth
		}
		started, _ := load()
		started.StartAutoRefresh(t.Context(), time.Minute)
		defer started.StopAutoRefresh()
		time.Sleep(time.Second)
		synctest.Wait()
		started.StopAutoRefresh()
		if active, _ := started.GetByID(name); !active.RequiresSignIn() || provider.refreshCalls != 1 {
			t.Fatalf("initial exact refresh failure was not recorded, exchanges=%d", provider.refreshCalls)
		}

		restarted, restored := load()
		if !restored.RequiresSignIn() || restored.SignInBlocksDispatch(time.Now()) || restored.Disabled {
			t.Fatal("restart lost the expiry state or prevented still-valid access")
		}
		if current, errRead := os.ReadFile(path); errRead != nil || !bytes.Equal(current, input) {
			t.Fatalf("recording the sign-in state changed the credential file: %v", errRead)
		}
		restarted.StartAutoRefresh(t.Context(), time.Minute)
		defer restarted.StopAutoRefresh()
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if provider.refreshCalls != 1 {
			t.Fatalf("restored account sent another refresh, exchanges=%d", provider.refreshCalls)
		}
		if _, errExecute := restarted.Execute(t.Context(), []string{"claude"}, executor.Request{Model: model}, executor.Options{}); errExecute == nil {
			t.Fatal("the fixture Messages request should have returned its verified rejection")
		}
		restarted.StopAutoRefresh()
		if provider.messageCalls != 1 || provider.refreshCalls != 1 {
			t.Fatalf("verified Messages rejection caused unexpected dispatch: messages=%d refresh=%d", provider.messageCalls, provider.refreshCalls)
		}

		rejected, rejectedAuth := load()
		if !rejectedAuth.HasValidAccessToken(time.Now()) || !rejectedAuth.SignInBlocksDispatch(time.Now()) || rejectedAuth.Disabled {
			t.Fatal("restored 401 should block the same access token without setting manual disable")
		}
		_, errDisable := rejected.PatchAuth(t.Context(), name, rejectedAuth.RegistrationEpoch, func(auth *cliproxyauth.Auth) error {
			auth.Disabled, auth.Status = true, cliproxyauth.StatusDisabled
			return nil
		})
		if errDisable != nil {
			t.Fatal(errDisable)
		}
		disabled, disabledAuth := load()
		if !disabledAuth.RequiresSignIn() || !disabledAuth.Disabled {
			t.Fatal("manual disable and the sign-in state must survive independently")
		}
		_, errLogin := disabled.PatchAuth(t.Context(), name, disabledAuth.RegistrationEpoch, func(auth *cliproxyauth.Auth) error {
			auth.Metadata["access_token"] = "fixture-new-access"
			auth.Metadata["refresh_token"] = "fixture-new-refresh"
			return nil
		})
		if errLogin != nil {
			t.Fatal(errLogin)
		}
		_, replaced := load()
		if replaced.RequiresSignIn() || !replaced.Disabled {
			t.Fatal("genuine relogin inherited the old marker or cleared manual disable")
		}
	})
}
