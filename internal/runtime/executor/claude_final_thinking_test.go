package executor

import (
	"context"
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeFinalThinkingAfterPayloadOverride(t *testing.T) {
	for _, path := range []string{"execute", "stream"} {
		for _, tt := range []struct {
			name        string
			initial     string
			initialMode string
			model       string
			mode        string
			bound       *registry.ModelInfo
			wantError   bool
			customBase  bool
		}{
			{name: "sonnet disabled", initial: "claude-sonnet-5", model: "claude-sonnet-5-5", mode: "disabled", wantError: true},
			{name: "opus always on", initial: "claude-sonnet-5", model: "claude-opus-5-5", mode: "disabled", wantError: true},
			{name: "fable manual thinking", initial: "claude-sonnet-5", model: "claude-fable-5-1", mode: "enabled", wantError: true},
			{name: "valid between tools", initial: "claude-sonnet-5", model: "claude-sonnet-5-5", mode: "between_tools"},
			{name: "unknown model", initial: "claude-sonnet-5", model: "claude-custom-final", mode: "disabled"},
			{name: "future native between tools", initial: "claude-sonnet-5-50", model: "claude-sonnet-5-50", mode: "between_tools", bound: &registry.ModelInfo{ID: "claude-sonnet-5-50", Type: "claude"}},
			{name: "selected user defined", initial: "claude-sonnet-5-5", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-sonnet-5-5", Type: "claude", UserDefined: true}},
			{name: "selected user defined suffix", initial: "claude-sonnet-5-5", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-sonnet-5-5(none)", Type: "claude", UserDefined: true}},
			{name: "selected native suffix", initial: "claude-sonnet-5-5", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-sonnet-5-5(none)", Type: "claude"}, wantError: true},
			{name: "selected compat", initial: "claude-sonnet-5-5", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-sonnet-5-5", Type: "claude", IsCompat: true}},
			{name: "initial compat disabled", initial: "claude-opus-5-5", initialMode: "disabled", model: "claude-opus-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-opus-5-5", Type: "claude", IsCompat: true, Thinking: &registry.ThinkingSupport{ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}}}},
			{name: "initial compat between tools", initial: "claude-haiku-5-5", initialMode: "between_tools", model: "claude-haiku-5-5", mode: "between_tools", bound: &registry.ModelInfo{ID: "claude-haiku-5-5", Type: "claude", IsCompat: true, Thinking: &registry.ThinkingSupport{ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"low", "medium", "high"}}}},
			{name: "selected compat metadata only", initial: "claude-sonnet-5-5", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{IsCompat: true}},
			{name: "stale user defined binding", initial: "claude-custom-initial", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-custom-initial", Type: "claude", UserDefined: true}, wantError: true},
			{name: "stale compat binding", initial: "claude-custom-initial", model: "claude-opus-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-custom-initial", Type: "claude", IsCompat: true}, wantError: true},
			{name: "stale compat metadata only", initial: "claude-custom-initial", model: "claude-opus-5-5", mode: "disabled", bound: &registry.ModelInfo{IsCompat: true}, wantError: true},
			{name: "custom origin contract", initial: "claude-sonnet-5", model: "claude-sonnet-5-5", mode: "disabled", customBase: true},
		} {
			t.Run(path+"/"+tt.name, func(t *testing.T) {
				upstream := &midSystemUpstream{}
				cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
					Models: []config.PayloadModelRule{{Name: "*"}},
					Params: map[string]any{"model": tt.model, "thinking": map[string]any{"type": tt.mode}},
				}}}}
				auth := &cliproxyauth.Auth{ID: "final-thinking-fixture", Provider: "claude", Attributes: map[string]string{"api_key": "synthetic-final-thinking", "cloak_mode": "never"}}
				if tt.customBase {
					auth.Attributes["base_url"] = "https://compatible.example.invalid"
				}
				req := cliproxyexecutor.Request{Model: tt.initial, Payload: []byte(`{"model":"` + tt.initial + `","max_tokens":256,"messages":[{"role":"user","content":"hi"}]}`)}
				if tt.initialMode != "" {
					req.Payload, _ = sjson.SetBytes(req.Payload, "thinking.type", tt.initialMode)
				}
				if tt.bound != nil {
					req.Metadata = map[string]any{claudeReplayResolvedModelInfoKey: tt.bound}
				}
				errRun := runFinalThinkingExecutor(path, NewClaudeExecutor(cfg), upstream.context(t, nil), auth, req)
				if tt.wantError {
					assertFinalThinkingRequestError(t, errRun, upstream.called)
					return
				}
				if errRun != nil || !upstream.called {
					t.Fatalf("allowed request failed: dispatched=%v error=%v", upstream.called, errRun)
				}
				if got := gjson.GetBytes(upstream.body, "thinking.type").String(); got != tt.mode {
					t.Fatalf("final validator rewrote thinking.type=%q, want %q", got, tt.mode)
				}
			})
		}
	}
}

func TestClaudeFinalThinkingCountTokens(t *testing.T) {
	for _, tt := range []struct {
		name      string
		model     string
		mode      string
		bound     *registry.ModelInfo
		wantError bool
	}{
		{name: "valid between tools", model: "claude-sonnet-5-5", mode: "between_tools"},
		{name: "unknown model", model: "claude-custom-count", mode: "disabled"},
		{name: "selected user defined", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-sonnet-5-5", UserDefined: true}},
		{name: "stale binding cannot skip final model", model: "claude-sonnet-5-5", mode: "disabled", bound: &registry.ModelInfo{ID: "claude-old-count", UserDefined: true}, wantError: true},
		{name: "always on rejects none", model: "claude-opus-5-5", mode: "disabled", wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "synthetic-final-count", "cloak_mode": "never"}}
			req := cliproxyexecutor.Request{Model: tt.model, Payload: []byte(`{"model":"` + tt.model + `","thinking":{"type":"` + tt.mode + `"},"messages":[{"role":"user","content":"hi"}]}`)}
			if tt.bound != nil {
				req.Metadata = map[string]any{claudeReplayResolvedModelInfoKey: tt.bound}
			}
			errRun := runFinalThinkingExecutor("count", NewClaudeExecutor(&config.Config{}), upstream.context(t, nil), auth, req)
			if tt.wantError {
				assertFinalThinkingRequestError(t, errRun, upstream.called)
				return
			}
			if errRun != nil || !upstream.called {
				t.Fatalf("allowed count request failed: dispatched=%v error=%v", upstream.called, errRun)
			}
		})
	}
}

func runFinalThinkingExecutor(path string, executor *ClaudeExecutor, ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request) error {
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
	switch path {
	case "count":
		_, err := executor.CountTokens(ctx, auth, req, opts)
		return err
	case "stream":
		req.Payload, _ = sjson.SetBytes(req.Payload, "stream", true)
		opts.Stream = true
		result, err := executor.ExecuteStream(ctx, auth, req, opts)
		if err != nil {
			return err
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				return chunk.Err
			}
		}
		return nil
	default:
		_, err := executor.Execute(ctx, auth, req, opts)
		return err
	}
}

func assertFinalThinkingRequestError(t *testing.T, err error, dispatched bool) {
	t.Helper()
	var status interface{ StatusCode() int }
	var scoped interface{ IsRequestScoped() bool }
	if !errors.As(err, &status) || status.StatusCode() != 400 || !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("expected request-scoped 400, got %v", err)
	}
	if dispatched {
		t.Fatal("invalid final thinking request reached the upstream transport")
	}
}
