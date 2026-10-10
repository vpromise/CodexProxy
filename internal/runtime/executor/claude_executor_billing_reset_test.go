package executor

import (
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

func TestClaudeExecutor_AuthManager_OverageBillingBoundary(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		for _, tc := range []struct {
			name       string
			retryAfter string
			shared     bool
			min        time.Duration
			max        time.Duration
		}{
			{name: "generic backoff", min: time.Second, max: 30 * time.Minute},
			{name: "short retry", retryAfter: "60", min: time.Minute, max: 90 * time.Second},
			{name: "long explicit retry", retryAfter: "777600", min: 9 * 24 * time.Hour, max: 9*24*time.Hour + 30*time.Second},
			{name: "shared rejection", retryAfter: "60", shared: true, min: 2*time.Hour - time.Second, max: 2*time.Hour + 30*time.Second},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var calls atomic.Int32
				billing := strconv.FormatInt(time.Now().Add(23*24*time.Hour).Unix(), 10)
				sharedReset := strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
					w.Header().Set("Anthropic-Ratelimit-Unified-Representative-Claim", "overage")
					w.Header().Set("Anthropic-Ratelimit-Unified-Reset", billing)
					w.Header().Set("Anthropic-Ratelimit-Unified-Overage-Reset", billing)
					if tc.retryAfter != "" {
						w.Header().Set("Retry-After", tc.retryAfter)
					}
					if tc.shared {
						w.Header().Set("Anthropic-Ratelimit-Unified-5h-Status", "rejected")
						w.Header().Set("Anthropic-Ratelimit-Unified-5h-Reset", sharedReset)
					}
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Overage rejected."}}`))
				}))
				t.Cleanup(server.Close)
				manager := cliproxyauth.NewManager(nil, nil, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(NewClaudeExecutor(&config.Config{}))
				auth := &cliproxyauth.Auth{ID: uuid.NewString(), Provider: "claude", Attributes: map[string]string{"api_key": "test-key", "base_url": server.URL}}
				reg := registry.GetGlobalRegistry()
				reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-sonnet-4-6"}, {ID: "claude-opus-4-6"}})
				t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
				if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}
				req := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: []byte(`{"messages":[{"role":"user","content":"test"}]}`)}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				before := time.Now()
				var errExecute error
				if mode == "stream" {
					_, errExecute = manager.ExecuteStream(t.Context(), []string{"claude"}, req, opts)
				} else {
					_, errExecute = manager.Execute(t.Context(), []string{"claude"}, req, opts)
				}
				if errExecute == nil || calls.Load() != 1 {
					t.Fatalf("error/calls = %v/%d, want rejection/1", errExecute, calls.Load())
				}
				state, ok := manager.GetByID(auth.ID)
				if !ok || state == nil || state.Quota.Reason != "credential_quota" {
					t.Fatalf("missing conservative credential cooldown: %+v", state)
				}
				if state.NextRetryAfter.Before(before.Add(tc.min)) || state.NextRetryAfter.After(time.Now().Add(tc.max)) {
					t.Fatalf("cooldown = %v, want between %v and %v", state.NextRetryAfter.Sub(before), tc.min, tc.max)
				}
				if tc.retryAfter == "" {
					if state.Quota.BackoffLevel == 0 {
						t.Fatal("missing reset did not enter generic quota backoff")
					}
					return
				}
				req.Model = "claude-opus-4-6"
				if _, errSibling := manager.Execute(t.Context(), []string{"claude"}, req, opts); errSibling == nil || calls.Load() != 1 {
					t.Fatalf("sibling escaped credential cooldown: error/calls = %v/%d", errSibling, calls.Load())
				}
			})
		}
	}
}
