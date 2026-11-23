package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestManagerStreamCancelClosesWhileUpstreamStalls(t *testing.T) {
	source := make(chan cliproxyexecutor.StreamChunk, 1)
	source <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
	defer close(source)
	executor := &claudeCancellationTestExecutor{
		streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
			return &cliproxyexecutor.StreamResult{Chunks: source}, nil
		},
	}
	hook := &resultCaptureHook{}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, hook)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case chunk, ok := <-stream.Chunks:
		if !ok || string(chunk.Payload) != "first" || chunk.Err != nil {
			t.Fatalf("first chunk = %#v, open = %v", chunk, ok)
		}
	case <-time.After(time.Second):
		t.Fatal("first chunk was never delivered")
	}
	cancel()
	requireManagerStreamClosed(t, stream.Chunks)
	if results := hook.Results(); len(results) != 0 {
		t.Fatalf("canceled stream with no upstream result recorded %#v", results)
	}
	requireClaudeCancellationNeutral(t, manager, auth.ID, model)
}

func TestManagerStreamTerminalErrorCancelsAttemptAndReleasesAdmission(t *testing.T) {
	source := make(chan cliproxyexecutor.StreamChunk, 2)
	defer close(source)
	source <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
	terminal := &Error{HTTPStatus: http.StatusBadGateway, Message: "stream ended"}
	source <- cliproxyexecutor.StreamChunk{Err: terminal}
	var attemptCtx context.Context
	executor := &claudeCancellationTestExecutor{
		streamFn: func(ctx context.Context, _ *Auth) (*cliproxyexecutor.StreamResult, error) {
			attemptCtx = ctx
			return &cliproxyexecutor.StreamResult{Chunks: source}, nil
		},
	}
	hook := &resultCaptureHook{}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, hook)
	cfg := &internalconfig.Config{}
	cfg.ClaudeCode.MaxInFlight = 1
	manager.SetConfig(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream, err := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	if release, _, acquired := manager.tryAcquireLocalCredential(auth); acquired {
		release()
		t.Fatal("credential had a free admission slot while the stream was active")
	}
	for _, want := range []cliproxyexecutor.StreamChunk{{Payload: []byte("first")}, {Err: terminal}} {
		select {
		case chunk := <-stream.Chunks:
			if string(chunk.Payload) != string(want.Payload) || chunk.Err != want.Err {
				t.Fatalf("chunk = %#v, want %#v", chunk, want)
			}
		case <-time.After(time.Second):
			t.Fatal("stream did not deliver the expected chunk")
		}
	}
	requireManagerStreamClosed(t, stream.Chunks)
	if !errors.Is(attemptCtx.Err(), context.Canceled) || ctx.Err() != nil {
		t.Fatalf("terminal attempt = %v, caller context = %v", attemptCtx.Err(), ctx.Err())
	}
	release, _, acquired := manager.tryAcquireLocalCredential(auth)
	if !acquired {
		t.Fatal("stream did not release its admission slot")
	}
	defer release()
	if extra, _, acquiredExtra := manager.tryAcquireLocalCredential(auth); acquiredExtra {
		extra()
		t.Fatal("more than one admission slot was available")
	}
	results := hook.Results()
	if len(results) != 1 || results[0].Success || results[0].Error.StatusCode() != http.StatusBadGateway {
		t.Fatalf("results = %#v, want one terminal failure", results)
	}
}

type streamResultSignalHook struct {
	resultCaptureHook
	recorded chan struct{}
}

func (h *streamResultSignalHook) OnResult(ctx context.Context, result Result) {
	h.resultCaptureHook.OnResult(ctx, result)
	select {
	case h.recorded <- struct{}{}:
	default:
	}
}

func TestManagerStreamCancelRecordsObservedQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)
	for _, tc := range []struct {
		name             string
		direct           bool
		started          bool
		credentialScoped bool
		retryAfter       time.Duration
	}{
		{name: "direct status", direct: true, retryAfter: time.Hour, credentialScoped: true},
		{name: "bootstrap chunk without Retry-After"},
		{name: "first payload still waiting for consumer", started: true, retryAfter: time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hook := &streamResultSignalHook{recorded: make(chan struct{}, 1)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var retryAfter *time.Duration
			if tc.retryAfter > 0 {
				retryAfter = &tc.retryAfter
			}
			upstreamErr := streamQuotaError{
				customStatusError: customStatusError{code: http.StatusTooManyRequests, msg: "upstream quota", retryAfter: retryAfter},
				credentialScoped:  tc.credentialScoped,
			}
			source := make(chan cliproxyexecutor.StreamChunk, 2)
			executor := &claudeCancellationTestExecutor{
				streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
					if tc.started {
						source <- cliproxyexecutor.StreamChunk{Payload: []byte("first")}
					}
					if tc.direct {
						cancel()
						return nil, upstreamErr
					}
					source <- cliproxyexecutor.StreamChunk{Err: upstreamErr}
					if !tc.started {
						cancel()
					}
					return &cliproxyexecutor.StreamResult{Chunks: source}, nil
				},
			}
			defer close(source)
			manager, auth, model := newClaudeCancellationTestManager(t, executor, hook)
			before := time.Now()

			stream, err := manager.ExecuteStream(ctx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if tc.started {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				select {
				case <-hook.recorded:
				case <-time.After(time.Second):
					t.Fatal("stream did not account for the ready 429 while the downstream was stalled")
				}
				requireManagerStreamClosed(t, stream.Chunks)
			} else if stream != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("stream = %v, error = %v, want context.Canceled", stream, err)
			}
			if got := executor.streamCalls.Load(); got != 1 {
				t.Fatalf("upstream calls = %d, want 1", got)
			}
			results := hook.Results()
			if len(results) != 1 || results[0].Success || results[0].Error.StatusCode() != http.StatusTooManyRequests ||
				results[0].CredentialScope != tc.credentialScoped {
				t.Fatalf("results = %#v, want one 429 with credential scope = %v", results, tc.credentialScoped)
			}
			updated, _ := manager.GetByID(auth.ID)
			state := updated.ModelStates[model]
			if state == nil || !state.Quota.Exceeded || !state.NextRetryAfter.After(before) ||
				state.NextRetryAfter.Before(before.Add(tc.retryAfter)) {
				t.Fatalf("model cooldown did not preserve upstream 429 and Retry-After: %+v", state)
			}
			if tc.credentialScoped && (updated.Quota.Reason != "credential_quota" || updated.Quota.NextRecoverAt.Before(before.Add(tc.retryAfter))) {
				t.Fatalf("credential cooldown lost upstream scope or Retry-After: %+v", updated.Quota)
			}
			if !tc.credentialScoped && updated.Quota.Reason == "credential_quota" {
				t.Fatalf("model limit became credential cooldown: %+v", updated.Quota)
			}
		})
	}
}

func TestManagerStreamRefreshReplacesAbandonedAttempt(t *testing.T) {
	for _, bootstrap := range []bool{false, true} {
		name := "direct"
		if bootstrap {
			name = "bootstrap"
		}
		t.Run(name, func(t *testing.T) {
			callerCtx, cancelCaller := context.WithCancel(context.Background())
			defer cancelCaller()
			var oldContext, newContext context.Context
			unauthorized := &Error{HTTPStatus: http.StatusUnauthorized, Message: "upstream unauthorized"}
			failedSource := make(chan cliproxyexecutor.StreamChunk, 1)
			defer close(failedSource)
			executor := &claudeCancellationTestExecutor{
				streamFn: func(ctx context.Context, auth *Auth) (*cliproxyexecutor.StreamResult, error) {
					if authAccessToken(auth) != "refreshed-access-token" {
						oldContext = ctx
						logging.SetResponseHeaders(ctx, http.Header{"X-Attempt": {"old"}})
						if !bootstrap {
							return nil, unauthorized
						}
						failedSource <- cliproxyexecutor.StreamChunk{Err: unauthorized}
						return &cliproxyexecutor.StreamResult{Chunks: failedSource}, nil
					}
					newContext = ctx
					if headers := logging.GetResponseHeaders(ctx); len(headers) != 0 {
						t.Errorf("retry inherited rejected attempt headers: %v", headers)
					}
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					chunks := make(chan cliproxyexecutor.StreamChunk, 1)
					chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("fresh")}
					close(chunks)
					return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
				},
				refreshFn: func(ctx context.Context, auth *Auth) (*Auth, error) {
					if !errors.Is(oldContext.Err(), context.Canceled) {
						t.Error("old stream attempt was still active when credential refresh began")
					}
					if headers := logging.GetResponseHeaders(ctx); len(headers) != 0 {
						t.Errorf("refresh inherited rejected attempt headers: %v", headers)
					}
					if err := ctx.Err(); err != nil {
						return nil, err
					}
					auth.Metadata["access_token"] = "refreshed-access-token"
					return auth, nil
				},
			}
			hook := &resultCaptureHook{}
			manager, _, model := newClaudeCancellationTestManager(t, executor, hook)
			stream, err := manager.ExecuteStream(callerCtx, []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case chunk, ok := <-stream.Chunks:
				if !ok || string(chunk.Payload) != "fresh" || chunk.Err != nil {
					t.Fatalf("first chunk = %#v, open = %v", chunk, ok)
				}
			case <-time.After(time.Second):
				t.Fatal("refreshed attempt never delivered its first chunk")
			}
			requireManagerStreamClosed(t, stream.Chunks)
			if !errors.Is(newContext.Err(), context.Canceled) || callerCtx.Err() != nil {
				t.Fatalf("finished attempt = %v, caller context = %v", newContext.Err(), callerCtx.Err())
			}
			if got := executor.refreshCalls.Load(); got != 1 {
				t.Fatalf("refresh calls = %d, want 1", got)
			}
			results := hook.Results()
			if len(results) != 1 || !results[0].Success {
				t.Fatalf("results = %#v, want one success from the refreshed attempt", results)
			}
		})
	}
}

func requireManagerStreamClosed(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk) {
	t.Helper()
	select {
	case chunk, ok := <-chunks:
		if ok {
			t.Fatalf("expected closed stream, got %#v", chunk)
		}
	case <-time.After(time.Second):
		t.Fatal("stream remained open")
	}
}
