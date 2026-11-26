package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeNativeRequestValidatesBeforeNormalization(t *testing.T) {
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-2"} {
		for _, path := range []string{"execute", "stream", "count"} {
			for _, tt := range []struct {
				name, fields, apiVersion string
			}{
				{name: "manual budget", fields: `,"thinking":{"type":"enabled","budget_tokens":2048}`},
				{name: "adaptive with manual budget", fields: `,"thinking":{"type":"adaptive","budget_tokens":2048},"output_config":{"effort":"high"}`},
				{name: "non-native thinking type", fields: `,"thinking":{"type":"auto"},"output_config":{"effort":"high"}`},
				{name: "unsupported effort", fields: `,"thinking":{"type":"adaptive"},"output_config":{"effort":"ultra"}`},
				{name: "disabled", fields: `,"thinking":{"type":"disabled"}`},
				{name: "between tools", fields: `,"thinking":{"type":"between_tools"}`},
				{name: "forced tool", fields: `,"tool_choice":{"type":"any"}`},
				{name: "top k", fields: `,"top_k":2`},
				{name: "temperature", fields: `,"temperature":0.2`},
				{name: "top p", fields: `,"top_p":1`},
				{name: "multiple sampling fields", fields: `,"temperature":1,"top_p":0.99`},
				{name: "sampling removed in newer API", fields: `,"temperature":1`, apiVersion: "2026-10-21"},
			} {
				t.Run(model+"/"+path+"/"+tt.name, func(t *testing.T) {
					upstream := &midSystemUpstream{}
					cfg := &config.Config{DisableClaudeCloakMode: true}
					auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fixture-claude-native"}}
					req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]` + tt.fields + `}`)}
					headers := make(http.Header)
					if tt.apiVersion != "" {
						headers.Set("Anthropic-Version", tt.apiVersion)
					}
					errRun := runFinalThinkingExecutor(path, NewClaudeExecutor(cfg), upstream.context(t, headers), auth, req)
					assertFinalThinkingRequestError(t, errRun, upstream.called)
				})
			}
		}
	}
}

func TestClaudeNativeCachePrewarming(t *testing.T) {
	const model = "claude-opus-5-5"
	const payload = `{"model":"claude-opus-5-5","max_tokens":0,
		"system":[{"type":"text","text":"cached context","cache_control":{"type":"ephemeral","ttl":"1h"}}],
		"messages":[{"role":"user","content":"hi"}]`
	for _, tt := range []struct {
		name, path, fields string
		wantError          bool
	}{
		{name: "prewarm", path: "execute"},
		{name: "explicit stream flag", path: "execute", fields: `,"stream":true`, wantError: true},
		{name: "stream entry point", path: "stream", wantError: true},
		{name: "structured output", path: "execute", fields: `,"output_config":{"format":{"type":"json_schema","schema":{"type":"object"}}}`, wantError: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			cfg := &config.Config{DisableClaudeCloakMode: true}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fixture-claude-native"}}
			req := cliproxyexecutor.Request{Model: model, Payload: []byte(payload + tt.fields + "}")}
			err := runFinalThinkingExecutor(tt.path, NewClaudeExecutor(cfg), upstream.context(t, nil), auth, req)
			if tt.wantError {
				assertFinalThinkingRequestError(t, err, upstream.called)
				return
			}
			if err != nil || !upstream.called {
				t.Fatalf("valid prewarm request failed: dispatched=%v error=%v", upstream.called, err)
			}
			if got := gjson.GetBytes(upstream.body, "max_tokens"); got.Type != gjson.Number || got.Num != 0 {
				t.Fatalf("max_tokens = %s, want explicit zero", got.Raw)
			}
			if got := gjson.GetBytes(upstream.body, "system.0.cache_control.ttl").String(); got != "1h" {
				t.Fatalf("cache TTL = %q, want 1h", got)
			}
		})
	}
}

func TestClaudeNativeRequestPreservesAPIVersion(t *testing.T) {
	auth := midSystemAuth()
	executor := NewClaudeExecutor(midSystemConfig())
	for _, tt := range []struct {
		version, fields string
	}{
		{version: "2023-06-01", fields: `,"temperature":1`},
		{version: "2023-06-01", fields: `,"top_p":0.99`},
		{version: "2026-10-21"},
	} {
		upstream := &midSystemUpstream{}
		headers := make(http.Header)
		headers.Set("Anthropic-Version", tt.version)
		req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":4096,"messages":[{"role":"user","content":"hi"}]` + tt.fields + "}")}
		if err := runFinalThinkingExecutor("execute", executor, upstream.context(t, headers), auth, req); err != nil {
			t.Fatalf("valid request for version %s with fields %q failed: %v", tt.version, tt.fields, err)
		}
		if got := helps.HeaderValueCaseInsensitive(upstream.headers, "Anthropic-Version"); got != tt.version {
			t.Errorf("upstream API version = %q, want %q", got, tt.version)
		}
	}
}

func TestClaudeNativeRequestHonorsThinkingSuffix(t *testing.T) {
	const model = "claude-opus-5-5"
	req := cliproxyexecutor.Request{
		Model:   model + "(high)",
		Payload: []byte(`{"model":"claude-opus-5-5(high)","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"disabled"}}`),
	}
	auth := midSystemAuth()
	for _, route := range []string{"execute", "stream", "count"} {
		t.Run(route, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			executor := NewClaudeExecutor(&config.Config{DisableClaudeCloakMode: true})
			if err := runFinalThinkingExecutor(route, executor, upstream.context(t, nil), auth, req); err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(upstream.body, "thinking.type").String(); got != "adaptive" {
				t.Errorf("upstream thinking.type = %q, want adaptive from suffix", got)
			}
		})
	}
}

func TestClaudeNativeRequestUsesCredentialAPIVersion(t *testing.T) {
	for _, tt := range []struct {
		incoming, configured, wantVersion string
		wantError                         bool
	}{
		{incoming: "2026-10-21", configured: "2023-06-01", wantVersion: "2023-06-01"},
		{incoming: "2023-06-01", configured: "$X-Configured-Version", wantVersion: "2026-10-21", wantError: true},
	} {
		t.Run(tt.configured, func(t *testing.T) {
			headers := http.Header{"Anthropic-Version": {tt.incoming}, "X-Configured-Version": {tt.wantVersion}}
			upstream := &midSystemUpstream{}
			executor := NewClaudeExecutor(midSystemConfig())
			auth := midSystemAuth()
			auth.Attributes["header:anthropic-version"] = tt.configured
			req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"temperature":1}`)}
			err := runFinalThinkingExecutor("execute", executor, upstream.context(t, headers), auth, req)
			if tt.wantError {
				assertFinalThinkingRequestError(t, err, upstream.called)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := helps.HeaderValueCaseInsensitive(upstream.headers, "Anthropic-Version"); got != tt.wantVersion {
				t.Errorf("upstream API version = %q, want %q", got, tt.wantVersion)
			}
		})
	}
}

func TestClaudeTranslatedRequestKeepsFixedAPIVersion(t *testing.T) {
	const model = "claude-opus-5"
	body := []byte(`{"model":"claude-opus-5","max_tokens":128,"messages":[{"role":"user","content":"hi"}]}`)
	upstream := &midSystemUpstream{}
	headers := http.Header{"Anthropic-Version": {"2026-10-21"}}
	executor := NewClaudeExecutor(midSystemConfig())
	req := cliproxyexecutor.Request{Model: model, Payload: body}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}
	if _, err := executor.Execute(upstream.context(t, headers), midSystemAuth(), req, opts); err != nil {
		t.Fatal(err)
	}
	if got := helps.HeaderValueCaseInsensitive(upstream.headers, "Anthropic-Version"); got != "2023-06-01" {
		t.Errorf("translated upstream API version = %q, want fixed CLI version", got)
	}
}

func TestClaudeNativeForcedToolHonorsThinking(t *testing.T) {
	for _, tt := range []struct {
		model, thinking string
		wantError       bool
	}{
		{model: "claude-opus-5", thinking: `{"type":"adaptive"}`},
		{model: "claude-sonnet-4-6", thinking: `{"type":"enabled","budget_tokens":2048}`, wantError: true},
	} {
		for _, route := range []string{"execute", "stream"} {
			t.Run(tt.model+"/"+route, func(t *testing.T) {
				upstream := &midSystemUpstream{}
				executor := NewClaudeExecutor(&config.Config{DisableClaudeCloakMode: true})
				auth := midSystemAuth()
				auth.Attributes["cloak_mode"] = "never"
				req := cliproxyexecutor.Request{Model: tt.model, Payload: []byte(`{"model":"` + tt.model + `","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"},"output_config":{"effort":"high"},"thinking":` + tt.thinking + `}`)}
				err := runFinalThinkingExecutor(route, executor, upstream.context(t, nil), auth, req)
				if tt.wantError {
					assertFinalThinkingRequestError(t, err, upstream.called)
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if values := gjson.GetManyBytes(upstream.body, "thinking.type", "output_config.effort"); values[0].String() != "adaptive" || values[1].String() != "high" {
					t.Errorf("upstream adaptive thinking or effort was removed: %s", upstream.body)
				}
			})
		}
	}
}

func TestClaudeNativeRequestValidatesEffectiveModel(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"model": "claude-opus-4-6"},
	}}}})
	req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`)}
	for _, route := range []string{"execute", "stream"} {
		t.Run(route, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			if err := runFinalThinkingExecutor(route, executor, upstream.context(t, nil), midSystemAuth(), req); err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(upstream.body, "model").String(); got != "claude-opus-4-6" {
				t.Errorf("upstream model = %q, want configured override", got)
			}
		})
	}
}

func TestClaudeNativeRequestHonorsPayloadOptionOverride(t *testing.T) {
	executor := NewClaudeExecutor(&config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*"}},
		Params: map[string]any{"tool_choice.type": "auto"},
	}}}})
	req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"read","input_schema":{"type":"object"}}],"tool_choice":{"type":"any"}}`)}
	for _, route := range []string{"execute", "stream"} {
		t.Run(route, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			if err := runFinalThinkingExecutor(route, executor, upstream.context(t, nil), midSystemAuth(), req); err != nil {
				t.Fatal(err)
			}
			if got := gjson.GetBytes(upstream.body, "tool_choice.type").String(); got != "auto" {
				t.Errorf("upstream tool_choice.type = %q, want configured override", got)
			}
		})
	}
}

func TestClaudeNativePrewarmRejectsManualThinkingSuffix(t *testing.T) {
	upstream := &midSystemUpstream{}
	executor := NewClaudeExecutor(&config.Config{})
	req := cliproxyexecutor.Request{Model: "claude-opus-4-6(2048)", Payload: []byte(`{"model":"claude-opus-4-6(2048)","max_tokens":0,"messages":[{"role":"user","content":"warmup"}]}`)}
	err := runFinalThinkingExecutor("execute", executor, upstream.context(t, nil), midSystemAuth(), req)
	assertFinalThinkingRequestError(t, err, upstream.called)
}

func TestClaudeNativeLegacyRejectsConflictingSampling(t *testing.T) {
	upstream := &midSystemUpstream{}
	executor := NewClaudeExecutor(&config.Config{})
	req := cliproxyexecutor.Request{Model: "claude-haiku-4-5-20251001", Payload: []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":128,"messages":[{"role":"user","content":"hi"}],"temperature":0.7,"top_p":0.99}`)}
	auth := midSystemAuth()
	auth.Attributes["cloak_mode"] = "never"
	err := runFinalThinkingExecutor("execute", executor, upstream.context(t, nil), auth, req)
	assertFinalThinkingRequestError(t, err, upstream.called)
}

func TestClaudeNativeLegacyThinkingRejectsIncompatibleOptions(t *testing.T) {
	for _, fields := range []string{
		`"thinking":{"type":"adaptive"},"top_p":0.5`,
		`"thinking":{"type":"adaptive"},"top_k":2`,
		`"thinking":{"type":"disabled","block_binding":{"prefix_mismatch_behavior":"error"}}`,
	} {
		upstream := &midSystemUpstream{}
		executor := NewClaudeExecutor(&config.Config{})
		req := cliproxyexecutor.Request{Model: "claude-sonnet-4-6", Payload: []byte(`{"model":"claude-sonnet-4-6","max_tokens":4096,"messages":[{"role":"user","content":"hi"}],` + fields + "}")}
		err := runFinalThinkingExecutor("execute", executor, upstream.context(t, nil), midSystemAuth(), req)
		assertFinalThinkingRequestError(t, err, upstream.called)
	}
}

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
		opts.OriginalRequest = req.Payload
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
