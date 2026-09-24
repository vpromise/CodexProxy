package executor

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClaudeExecutor_SharedCooldownDoesNotInheritModelRetry(t *testing.T) {
	const fable = "claude-fable-5"
	const sonnet = "claude-sonnet-5"
	const opus = "claude-opus-5"
	modelRetry := 8 * 24 * time.Hour
	sharedRetry := 3 * time.Hour
	for _, streaming := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(streaming), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct{ Model string }
				if errDecode := json.NewDecoder(r.Body).Decode(&body); errDecode != nil {
					http.Error(w, "invalid test request", http.StatusBadRequest)
					return
				}
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				w.Header().Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
				switch body.Model {
				case fable:
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
					w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
					w.Header().Set("Retry-After", strconv.FormatInt(int64(modelRetry/time.Second), 10))
				case sonnet:
					w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
					w.Header().Set("Retry-After", strconv.FormatInt(int64(sharedRetry/time.Second), 10))
				default:
					http.Error(w, "unexpected test model", http.StatusBadRequest)
					return
				}
				w.WriteHeader(http.StatusTooManyRequests)
				if _, errWrite := w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage window rejected."}}`)); errWrite != nil {
					t.Errorf("write test response: %v", errWrite)
				}
			}))
			t.Cleanup(server.Close)
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(0, 0, 0)
			manager.RegisterExecutor(NewClaudeExecutor(&config.Config{DisableCooling: false}))
			auth := &cliproxyauth.Auth{
				ID: uuid.NewString(), Provider: "claude",
				Attributes: map[string]string{"api_key": "test-token", "base_url": server.URL},
			}
			reg := registry.GetGlobalRegistry()
			reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: fable}, {ID: sonnet}, {ID: opus}})
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			for _, model := range []string{fable, sonnet, opus} {
				manager.MarkResult(t.Context(), cliproxyauth.Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, Success: true})
			}
			before := time.Now()
			for _, model := range []string{fable, sonnet} {
				req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"test"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: streaming}
				var errRequest error
				if streaming {
					_, errRequest = manager.ExecuteStream(t.Context(), []string{auth.Provider}, req, opts)
				} else {
					_, errRequest = manager.Execute(t.Context(), []string{auth.Provider}, req, opts)
				}
				var status interface{ StatusCode() int }
				if !errors.As(errRequest, &status) || status.StatusCode() != http.StatusTooManyRequests {
					t.Fatalf("%s request error = %v, want upstream 429", model, errRequest)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("upstream calls = %d, want two distinct model requests", calls.Load())
			}
			snapshot, _ := manager.GetByID(auth.ID)
			if snapshot.Quota.Reason != "credential_quota" || snapshot.Quota.NextRecoverAt.Before(before.Add(sharedRetry)) || snapshot.Quota.NextRecoverAt.After(time.Now().Add(sharedRetry+time.Minute)) {
				t.Fatalf("shared cooldown inherited the model retry: %+v", snapshot.Quota)
			}
			state := snapshot.ModelStates[fable]
			if state == nil || state.NextRetryAfter.Before(before.Add(modelRetry)) {
				t.Fatalf("Fable lost its own longer cooldown: %+v", state)
			}
		})
	}
}
