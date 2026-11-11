package thinking_test

import (
	"errors"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	"github.com/tidwall/gjson"
)

func TestClaudeModelThinkingModes(t *testing.T) {
	for _, tt := range []struct {
		name        string
		model       string
		body        string
		suffix      string
		wantType    string
		wantError   bool
		unchanged   bool
		userDefined bool
		isCompat    bool
	}{
		{name: "sonnet none suffix", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"adaptive","display":"updates","budget_tokens":4096,"block_binding":{"prefix_mismatch_behavior":"error"}},"output_config":{"effort":"max"}}`, suffix: "(none)", wantType: "between_tools"},
		{name: "sonnet native disabled", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"disabled"}}`, wantType: "between_tools"},
		{name: "sonnet native between tools", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"between_tools"},"output_config":{"effort":"high"}}`, wantType: "between_tools", unchanged: true},
		{name: "sonnet between tools max", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"between_tools"},"output_config":{"effort":"max"}}`, wantError: true},
		{name: "sonnet between tools display", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"between_tools","display":"updates"}}`, wantError: true},
		{name: "sonnet between tools binding", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"between_tools","block_binding":{"prefix_mismatch_behavior":"error"}}}`, wantError: true},
		{name: "sonnet between tools changed turn effort", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"between_tools"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi","output_config":{"effort":"medium"}}]}`, wantError: true},
		{name: "sonnet between tools same turn effort", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"between_tools"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi","output_config":{"effort":"high"}}]}`, wantType: "between_tools", unchanged: true},
		{name: "opus always on native none", model: "claude-opus-5-5", body: `{"thinking":{"type":"disabled"}}`, wantError: true},
		{name: "opus always on suffix none", model: "claude-opus-5-5", body: `{}`, suffix: "(none)", wantError: true},
		{name: "fable always on native none", model: "claude-fable-5-2", body: `{"thinking":{"type":"disabled"}}`, wantError: true},
		{name: "mythos always on native none", model: "claude-mythos-5-1", body: `{"thinking":{"type":"disabled"}}`, wantError: true},
		{name: "haiku disabled max", model: "claude-haiku-5-5", body: `{"thinking":{"type":"disabled"},"output_config":{"effort":"max"}}`, wantError: true},
		{name: "haiku disabled high", model: "claude-haiku-5-5", body: `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"}}`, wantType: "disabled"},
		{name: "haiku disabled same turn effort", model: "claude-haiku-5-5", body: `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi","output_config":{"effort":"high"}}]}`, wantType: "disabled", unchanged: true},
		{name: "haiku adaptive max", model: "claude-haiku-5-5", body: `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`, wantType: "adaptive", unchanged: true},
		{name: "haiku disabled changed turn effort", model: "claude-haiku-5-5", body: `{"thinking":{"type":"disabled"},"output_config":{"effort":"high"},"messages":[{"role":"user","content":"hi","output_config":{"effort":"medium"}}]}`, wantError: true},
		{name: "haiku cannot use between tools", model: "claude-haiku-5-5", body: `{"thinking":{"type":"between_tools"}}`, wantError: true},
		{name: "suffix overrides invalid body mode", model: "claude-opus-5-5", body: `{"thinking":{"type":"disabled"}}`, suffix: "(high)", wantType: "adaptive"},
		{name: "custom keeps own mode policy", model: "claude-opus-5-5", body: `{"thinking":{"type":"disabled"}}`, wantType: "disabled", userDefined: true},
		{name: "compat keeps disabled policy", model: "claude-opus-5-5", body: `{"thinking":{"type":"disabled"}}`, wantType: "disabled", isCompat: true},
		{name: "compat keeps between tools policy", model: "claude-haiku-5-5", body: `{"thinking":{"type":"between_tools"}}`, wantType: "between_tools", unchanged: true, isCompat: true},
		{name: "version boundary remains distinct", model: "claude-sonnet-5-50", body: `{"thinking":{"type":"disabled"}}`, wantType: "disabled"},
		{name: "future between tools remains upstream owned", model: "claude-sonnet-5-50", body: `{"thinking":{"type":"between_tools"}}`, wantType: "between_tools", unchanged: true},
		{name: "legacy sonnet none", model: "claude-sonnet-5", body: `{"thinking":{"type":"disabled"}}`, wantType: "disabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			info := &registry.ModelInfo{ID: tt.model, Type: "claude", UserDefined: tt.userDefined, IsCompat: tt.isCompat, Thinking: &registry.ThinkingSupport{ZeroAllowed: true, DynamicAllowed: true, Levels: []string{"low", "medium", "high", "xhigh", "max"}}}
			body := []byte(tt.body)
			out, errApply := thinking.ApplyThinkingWithModelInfo(body, body, tt.model+tt.suffix, "claude", "claude", "claude", info)
			if tt.wantError {
				if errApply == nil {
					t.Fatalf("expected invalid mode error, got %s", out)
				}
				var thinkingErr *thinking.ThinkingError
				if !errors.As(errApply, &thinkingErr) || thinkingErr.StatusCode() != 400 {
					t.Fatalf("error = %T %v, want thinking 400", errApply, errApply)
				}
				var scoped interface{ IsRequestScoped() bool }
				if !errors.As(errApply, &scoped) || !scoped.IsRequestScoped() {
					t.Fatalf("error must remain request-scoped: %v", errApply)
				}
				if string(out) != tt.body {
					t.Fatalf("error changed input body: %s", out)
				}
				return
			}
			if errApply != nil {
				t.Fatal(errApply)
			}
			if got := gjson.GetBytes(out, "thinking.type").String(); got != tt.wantType {
				t.Fatalf("thinking.type=%q, want %q; body=%s", got, tt.wantType, out)
			}
			if tt.unchanged && string(out) != tt.body {
				t.Fatalf("valid request changed: %s", out)
			}
			if tt.wantType == "between_tools" && len(gjson.GetBytes(out, "thinking").Map()) != 1 {
				t.Fatalf("between_tools must contain type only: %s", out)
			}
		})
	}
}

func TestClaudeModelThinkingModesRegistryEntry(t *testing.T) {
	for _, format := range []string{"claude", "openai", "codex"} {
		for _, model := range []string{"claude-sonnet-5-5", "claude-opus-5-5"} {
			t.Run(format+"/"+model, func(t *testing.T) {
				body := []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
				out, errApply := thinking.ApplyThinking(body, model+"(none)", format, "claude", "claude")
				if model == "claude-opus-5-5" {
					if errApply == nil || string(out) != string(body) {
						t.Fatalf("always-on none must fail without changing the input: body=%s error=%v", out, errApply)
					}
					return
				}
				if errApply != nil || gjson.GetBytes(out, "thinking.type").String() != "between_tools" {
					t.Fatalf("Sonnet none must use between_tools: body=%s error=%v", out, errApply)
				}
			})
		}
	}
}

func TestValidateClaudeFinalModelRequest(t *testing.T) {
	for _, tt := range []struct {
		name        string
		model       string
		body        string
		userDefined bool
		isCompat    bool
		wantError   bool
	}{
		{name: "late sonnet disabled", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"disabled"}}`, wantError: true},
		{name: "late fable manual budget", model: "claude-fable-5-2", body: `{"thinking":{"type":"enabled","budget_tokens":4096}}`, wantError: true},
		{name: "late opus none", model: "claude-opus-5-5", body: `{"thinking":{"type":"disabled"}}`, wantError: true},
		{name: "late haiku disabled binding", model: "claude-haiku-5-5", body: `{"thinking":{"type":"disabled","block_binding":{"prefix_mismatch_behavior":"error"}}}`, wantError: true},
		{name: "haiku adaptive max", model: "claude-haiku-5-5", body: `{"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}}`},
		{name: "haiku standalone max", model: "claude-haiku-5-5", body: `{"output_config":{"effort":"max"}}`},
		{name: "sonnet between tools", model: "claude-sonnet-5-5", body: `{"thinking":{"type":"between_tools"}}`},
		{name: "date suffix supported", model: "claude-sonnet-5-5-20261101", body: `{"thinking":{"type":"disabled"}}`, wantError: true},
		{name: "future version distinct", model: "claude-sonnet-5-50", body: `{"thinking":{"type":"disabled"}}`},
		{name: "future between tools remains upstream owned", model: "claude-sonnet-5-50", body: `{"thinking":{"type":"between_tools"}}`},
		{name: "compatible contract retained", model: "claude-fable-5-2", body: `{"thinking":{"type":"enabled","budget_tokens":4096}}`, userDefined: true},
		{name: "explicit compat disabled retained", model: "claude-opus-5-5", body: `{"thinking":{"type":"disabled"}}`, isCompat: true},
		{name: "explicit compat between tools retained", model: "claude-haiku-5-5", body: `{"thinking":{"type":"between_tools"}}`, isCompat: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			info := &registry.ModelInfo{ID: tt.model, Type: "claude", UserDefined: tt.userDefined, IsCompat: tt.isCompat}
			errValidate := thinking.ValidateClaudeModelRequest(body, info)
			if (errValidate != nil) != tt.wantError {
				t.Fatalf("error=%v, wantError=%v", errValidate, tt.wantError)
			}
			if string(body) != tt.body {
				t.Fatal("final validator modified the body")
			}
			if errUnknown := thinking.ValidateClaudeModelRequest(body, nil); errUnknown != nil {
				t.Fatalf("unknown model must retain upstream validation: %v", errUnknown)
			}
		})
	}
}
