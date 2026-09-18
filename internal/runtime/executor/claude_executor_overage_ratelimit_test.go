package executor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestClaudeExecutor_AuthManager_OverageSiblingAvailability(t *testing.T) {
	cases := []struct {
		name        string
		fiveHour    string
		weekly      string
		utilization string
		fable       string
		overage     string
		wantSibling bool
	}{
		{"existing Fable exception", "allowed", "allowed", "", "rejected", "", true},
		{"missing 5h status with healthy utilization", "", "allowed", "0.00", "rejected", "rejected", true},
		{"overage without Fable header", "allowed", "allowed", "", "", "rejected", true},
		{"shared 5h exhaustion", "rejected", "allowed", "1.0", "rejected", "rejected", false},
		{"shared weekly exhaustion", "allowed", "rejected", "0.00", "rejected", "rejected", false},
		{"missing health evidence", "", "allowed", "", "rejected", "rejected", false},
		{"invalid health evidence", "", "allowed", "NaN", "rejected", "rejected", false},
	}
	for _, mode := range []string{"execute", "stream"} {
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				var fableCalls, opusCalls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var payload struct {
						Model string `json:"model"`
					}
					if errDecode := json.NewDecoder(r.Body).Decode(&payload); errDecode != nil {
						http.Error(w, "invalid test request", http.StatusBadRequest)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					switch payload.Model {
					case "claude-fable-5":
						fableCalls.Add(1)
						w.Header().Set("Anthropic-Ratelimit-Unified-Status", "rejected")
						for suffix, value := range map[string]string{
							"5h-Status": tc.fiveHour, "7d-Status": tc.weekly,
							"5h-Utilization": tc.utilization, "7d_oi-Status": tc.fable,
							"Overage-Status": tc.overage,
						} {
							if value != "" {
								w.Header().Set("Anthropic-Ratelimit-Unified-"+suffix, value)
							}
						}
						w.Header().Set("Retry-After", "121180")
						w.WriteHeader(http.StatusTooManyRequests)
						_, _ = w.Write([]byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Usage window rejected."}}`))
					case "claude-opus-5":
						opusCalls.Add(1)
						_, _ = w.Write([]byte(`{"id":"msg-ok","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
					default:
						http.Error(w, "unexpected test model", http.StatusBadRequest)
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
				reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-fable-5"}, {ID: "claude-opus-5"}})
				t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
				if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}
				requestFor := func(model string) cliproxyexecutor.Request {
					return cliproxyexecutor.Request{Model: model, Payload: []byte(`{"messages":[{"role":"user","content":"test"}]}`)}
				}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				before := time.Now()
				var errFable error
				if mode == "stream" {
					_, errFable = manager.ExecuteStream(t.Context(), []string{"claude"}, requestFor("claude-fable-5"), opts)
				} else {
					_, errFable = manager.Execute(t.Context(), []string{"claude"}, requestFor("claude-fable-5"), opts)
				}
				if errFable == nil || fableCalls.Load() != 1 {
					t.Fatalf("Fable error/calls = %v/%d, want rejection/1", errFable, fableCalls.Load())
				}
				state, ok := manager.GetByID(auth.ID)
				if !ok || state == nil {
					t.Fatal("credential missing after request")
				}
				credentialBlocked := state.Quota.Reason == "credential_quota" && state.Quota.NextRecoverAt.After(time.Now())
				if credentialBlocked == tc.wantSibling {
					t.Fatalf("credential cooldown = %v, want %v", credentialBlocked, !tc.wantSibling)
				}
				if tc.wantSibling {
					model := state.ModelStates["claude-fable-5"]
					if model == nil || model.NextRetryAfter.Before(before.Add(121180*time.Second)) || model.NextRetryAfter.After(time.Now().Add(121215*time.Second)) {
						t.Fatalf("model cooldown did not preserve long upstream Retry-After: %+v", model)
					}
				}
				_, errOpus := manager.Execute(t.Context(), []string{"claude"}, requestFor("claude-opus-5"), opts)
				if tc.wantSibling {
					if errOpus != nil || opusCalls.Load() != 1 {
						t.Fatalf("healthy sibling error/calls = %v/%d, want nil/1", errOpus, opusCalls.Load())
					}
				} else if errOpus == nil || opusCalls.Load() != 0 {
					t.Fatalf("blocked sibling error/calls = %v/%d, want rejection/0", errOpus, opusCalls.Load())
				}
				_, errRetry := manager.Execute(t.Context(), []string{"claude"}, requestFor("claude-fable-5"), opts)
				if errRetry == nil || fableCalls.Load() != 1 {
					t.Fatalf("cooled model retried upstream: error/calls = %v/%d", errRetry, fableCalls.Load())
				}
			})
		}
	}
}

func TestClaudeExecutor_OveragePreservesRetryMetadataAcrossPaths(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "count tokens"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls.Add(1)
				wantPath := "/v1/messages"
				if mode == "count tokens" {
					wantPath += "/count_tokens"
				}
				if req.URL.Host != "api.anthropic.com" || req.URL.Path != wantPath {
					t.Errorf("upstream URL = %s, want Anthropic %s", req.URL, wantPath)
				}
				headers := make(http.Header)
				headers.Set("Anthropic-Ratelimit-Unified-Status", "rejected")
				headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.00")
				headers.Set("Anthropic-Ratelimit-Unified-7d-Status", "allowed")
				headers.Set("Anthropic-Ratelimit-Unified-Overage-Status", "rejected")
				headers.Set("Retry-After", "121180")
				headers.Set("Request-Id", "req-test-overage")
				return &http.Response{
					StatusCode: http.StatusTooManyRequests, Header: headers, Request: req,
					Body: io.NopCloser(strings.NewReader(`{"type":"error","error":{"type":"rate_limit_error","message":"Overage rejected."}}`)),
				}, nil
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			executor := NewClaudeExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{ID: uuid.NewString(), Provider: "claude", Attributes: map[string]string{"api_key": "test-api-key"}}
			req := cliproxyexecutor.Request{Model: "claude-fable-5", Payload: []byte(`{"messages":[{"role":"user","content":"test"}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			var err error
			switch mode {
			case "execute":
				_, err = executor.Execute(ctx, auth, req, opts)
			case "stream":
				_, err = executor.ExecuteStream(ctx, auth, req, opts)
			case "count tokens":
				_, err = executor.CountTokens(ctx, auth, req, opts)
			}
			var scoped interface{ IsCredentialScoped() bool }
			if err == nil || calls.Load() != 1 || !errors.As(err, &scoped) || scoped.IsCredentialScoped() {
				t.Fatalf("expected one model-scoped rejection, got error/calls = %v/%d", err, calls.Load())
			}
			var retry retryAfterProvider
			if !errors.As(err, &retry) || retry.RetryAfter() == nil || *retry.RetryAfter() < 121180*time.Second || *retry.RetryAfter() > 121210*time.Second {
				t.Fatalf("internal cooldown lost upstream Retry-After: %v", err)
			}
			var metadata interface {
				UpstreamRequestID() string
				DownstreamRetryAfter() *time.Duration
			}
			if !errors.As(err, &metadata) || metadata.UpstreamRequestID() != "req-test-overage" || metadata.DownstreamRetryAfter() == nil || *metadata.DownstreamRetryAfter() != 121180*time.Second {
				t.Fatalf("upstream request ID or downstream retry metadata lost: %v", err)
			}
		})
	}
}
