package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type claudeQuotaWarningExecutor struct {
	mockStreamErrorExecutor
	headers http.Header
}

func (e *claudeQuotaWarningExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	internallogging.SetResponseHeaders(ctx, e.headers)
	return cliproxyexecutor.Response{Payload: []byte("ok"), Headers: e.headers.Clone()}, nil
}

func newClaudeQuotaWarningManager(t *testing.T) (*Manager, *Auth, string) {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	model := "claude-warning-model"
	auth, errRegister := manager.Register(context.Background(), &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}, {ID: model + "-sibling"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	return manager, auth, model
}

func TestClaudeAllowedWarningKeepsSingleCredentialAvailable(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, window := range []string{"", "-5h", "-7d"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("window=%s/stream=%t", window, streaming), func(t *testing.T) {
				hook := setupTestLoggerHook(t)
				manager, auth, model := newClaudeQuotaWarningManager(t)
				store := &recordingCooldownStateStore{}
				manager.SetCooldownStateStore(store)
				headers := make(http.Header)
				headers.Set("Anthropic-Ratelimit-Unified"+window+"-Status", "allowed_warning")
				headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "0.92")
				headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "1800000000")
				headers.Set("Set-Cookie", "secret-cookie")
				var sources []chan cliproxyexecutor.StreamChunk
				var streams []*cliproxyexecutor.StreamResult
				finishStreams := func() {
					for _, source := range sources {
						close(source)
					}
					sources = nil
					for _, stream := range streams {
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								t.Errorf("stream failed: %v", chunk.Err)
							}
						}
					}
					streams = nil
				}
				t.Cleanup(finishStreams)
				manager.RegisterExecutor(&claudeQuotaWarningExecutor{
					headers: headers,
					mockStreamErrorExecutor: mockStreamErrorExecutor{
						identifier: "claude",
						executeStreamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
							internallogging.SetResponseHeaders(ctx, headers)
							chunks := make(chan cliproxyexecutor.StreamChunk, 1)
							chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok\n\n")}
							sources = append(sources, chunks)
							return &cliproxyexecutor.StreamResult{Headers: headers.Clone(), Chunks: chunks}, nil
						},
					},
				})
				for attempt := 1; attempt <= 3; attempt++ {
					ctx := internallogging.WithRequestID(context.Background(), fmt.Sprintf("warning-request-%d", attempt))
					if streaming {
						// Keep earlier streams open to exercise warning handling at response headers.
						stream, errStream := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
						if errStream != nil {
							t.Fatalf("request %d blocked by warning: %v", attempt, errStream)
						}
						streams = append(streams, stream)
						if chunk := <-stream.Chunks; chunk.Err != nil || len(chunk.Payload) == 0 {
							t.Fatalf("unexpected first chunk: %+v", chunk)
						}
					} else if _, errExecute := manager.Execute(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
						t.Fatalf("request %d blocked by warning: %v", attempt, errExecute)
					}
				}
				if streaming && len(hook.AllEntries()) != 3 {
					t.Error("stream warnings were not logged when response headers arrived")
				}
				finishStreams()
				updated, _ := manager.GetByID(auth.ID)
				state := updated.ModelStates[model]
				if updated.Success != 3 || updated.Unavailable || updated.Quota.Exceeded || !updated.NextRetryAfter.IsZero() || !modelStateIsClean(state) {
					t.Fatalf("warnings changed availability: auth=%+v state=%+v", updated, state)
				}
				if count := store.saveCount.Load(); count != 0 {
					t.Fatalf("warnings persisted cooldown state %d times", count)
				}
				entries := hook.AllEntries()
				if len(entries) != 3 {
					t.Fatalf("want one warning per response, got %d entries", len(entries))
				}
				wantWindow := strings.TrimPrefix(window, "-")
				if wantWindow == "" {
					wantWindow = "unified"
				}
				for i, entry := range entries {
					fields := entry.Data
					if fields["provider"] != "claude" || fields["model"] != model || fields["auth_index"] != auth.Index || fields["request_id"] != fmt.Sprintf("warning-request-%d", i+1) || fields["warning_windows"] != wantWindow || fields["five_hour_used_percent"] != float64(92) || fields["five_hour_reset_at"] != time.Unix(1800000000, 0).UTC().Format(time.RFC3339) {
						t.Errorf("warning is missing quota context: %+v", fields)
					}
					encoded, errMarshal := json.Marshal(fields)
					if errMarshal != nil || strings.Contains(string(encoded), "secret-cookie") || strings.Contains(string(encoded), auth.ID) {
						t.Errorf("unexpected warning fields: %s (error: %v)", encoded, errMarshal)
					}
					formatted, errFormat := (&internallogging.LogFormatter{}).Format(entry)
					if errFormat != nil {
						t.Fatal(errFormat)
					}
					for _, want := range []string{
						fmt.Sprintf("[%s]", fields["request_id"]),
						fmt.Sprintf("auth_index=%q", auth.Index),
						`quota_status="allowed_warning"`,
						fmt.Sprintf("warning_windows=%q", wantWindow),
						"five_hour_used_percent=92",
						"five_hour_reset_at=" + time.Unix(1800000000, 0).UTC().Format(time.RFC3339),
					} {
						if !strings.Contains(string(formatted), want) {
							t.Errorf("server log is missing %s: %s", want, formatted)
						}
					}
				}
			})
		}
	}
}

func TestClaudeAllowedWarningPreservesNewerFailureCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, tc := range []struct {
		name            string
		status          int
		credentialScope bool
	}{
		{"model quota", http.StatusTooManyRequests, false},
		{"credential quota", http.StatusTooManyRequests, true},
		{"overloaded", claudeUpstreamOverloadedStatus, false},
		{"unauthorized", http.StatusUnauthorized, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, auth, model := newClaudeQuotaWarningManager(t)
			headers := make(http.Header)
			headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed_warning")
			chunks := make(chan cliproxyexecutor.StreamChunk, 1)
			chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok\n\n")}
			manager.RegisterExecutor(&mockStreamErrorExecutor{
				identifier: "claude",
				executeStreamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					internallogging.SetResponseHeaders(ctx, headers)
					return &cliproxyexecutor.StreamResult{Headers: headers.Clone(), Chunks: chunks}, nil
				},
			})
			stream, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if errStream != nil {
				t.Fatal(errStream)
			}
			finishStream := func() {
				if chunks != nil {
					close(chunks)
					chunks = nil
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							t.Errorf("stream failed: %v", chunk.Err)
						}
					}
				}
			}
			t.Cleanup(finishStream)
			<-stream.Chunks

			// A different request fails while the earlier allowed stream is still open.
			ctx := internallogging.WithResponseHeadersHolder(context.Background())
			failureHeaders := headers.Clone()
			if tc.credentialScope {
				failureHeaders.Set("Anthropic-Ratelimit-Unified-7d-Status", "rejected")
			}
			internallogging.SetResponseHeaders(ctx, failureHeaders)
			retryAfter := time.Minute
			before := time.Now()
			manager.MarkResult(ctx, Result{
				AuthID: auth.ID, Provider: "claude", Model: model,
				Error:      &Error{HTTPStatus: tc.status, Message: tc.name},
				RetryAfter: &retryAfter, CredentialScope: tc.credentialScope, AttemptStartedAt: before,
			})
			failed, _ := manager.GetByID(auth.ID)
			deadline := failed.ModelStates[model].NextRetryAfter
			finishStream()
			updated, _ := manager.GetByID(auth.ID)
			state := updated.ModelStates[model]
			if !state.Unavailable || !state.NextRetryAfter.Equal(deadline) || state.NextRetryAfter.Before(before.Add(retryAfter)) || state.LastError == nil || state.LastError.HTTPStatus != tc.status {
				t.Fatalf("older warning stream cleared a newer failure: %+v", state)
			}
			if state.Quota.Exceeded != (tc.status == http.StatusTooManyRequests) {
				t.Errorf("failure classification changed: %+v", state.Quota)
			}
			if tc.credentialScope && (!updated.Quota.Exceeded || updated.Quota.Reason != "credential_quota" || !updated.Quota.NextRecoverAt.Equal(deadline)) {
				t.Errorf("credential quota cooldown changed: %+v", updated.Quota)
			}
			if _, _, _, errSelect := manager.pickNextMixed(ctx, []string{"claude"}, model, cliproxyexecutor.Options{}, nil); errSelect == nil {
				t.Error("failed model remained selectable during its cooldown")
			}
			if tc.status != http.StatusUnauthorized {
				_, _, _, errSelect := manager.pickNextMixed(ctx, []string{"claude"}, model+"-sibling", cliproxyexecutor.Options{}, nil)
				if (errSelect != nil) != tc.credentialScope {
					t.Errorf("sibling model availability lost cooldown scope: %v", errSelect)
				}
			}
		})
	}
}

func TestClaudeAllowedWarningLogsOnlyValidQuotaMetrics(t *testing.T) {
	hook := setupTestLoggerHook(t)
	headers := make(http.Header)
	headers.Set("Anthropic-Ratelimit-Unified-Status", " ALLOWED_WARNING ")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed_warning")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Utilization", "NaN")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Reset", "secret-header-value")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Utilization", "0.75")
	headers.Set("Anthropic-Ratelimit-Unified-7d-Reset", "1800000000")
	if logClaudeAllowedWarning(context.Background(), "codex", "model", "index", headers) {
		t.Fatal("Claude warning applied to another provider")
	}
	if !logClaudeAllowedWarning(context.Background(), "claude", "model", "index", headers) {
		t.Fatal("warning was not observed")
	}
	entries := hook.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("multiple window warnings should produce one log, got %d", len(entries))
	}
	fields := entries[0].Data
	if fields["warning_windows"] != "unified,5h" || fields["seven_day_used_percent"] != float64(75) || fields["seven_day_reset_at"] != time.Unix(1800000000, 0).UTC().Format(time.RFC3339) {
		t.Errorf("valid warning context lost: %+v", fields)
	}
	if _, ok := fields["five_hour_used_percent"]; ok {
		t.Error("invalid utilization logged")
	}
	if _, ok := fields["five_hour_reset_at"]; ok {
		t.Error("invalid reset time logged")
	}
	if _, errMarshal := json.Marshal(fields); errMarshal != nil {
		t.Fatalf("quota log fields cannot be encoded: %v", errMarshal)
	}
	hook.Reset()
	headers.Set("Anthropic-Ratelimit-Unified-Status", "allowed")
	headers.Set("Anthropic-Ratelimit-Unified-5h-Status", "allowed")
	if logClaudeAllowedWarning(context.Background(), "claude", "model", "index", headers) || len(hook.AllEntries()) != 0 {
		t.Error("ordinary allowed response produced a warning")
	}
}
