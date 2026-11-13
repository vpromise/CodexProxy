package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestClaudeExecutorSoftwareIdentityRemainsCoherent(t *testing.T) {
	for _, path := range []string{"messages", "stream", "count_tokens"} {
		t.Run(path, func(t *testing.T) {
			for _, tc := range []struct {
				name, mode, version, sdk, runtime, customUA string
				stabilize, omitBilling, rewriteMessage      bool
				configuredUA                                string
				wantVersion, wantSDK, wantRuntime           string
				wantEntrypoint                              string
			}{
				{name: "native legacy stabilized", mode: "auto", version: "2.1.280", sdk: "0.112.1", runtime: "v26.3.0", stabilize: true, wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "native never stabilized", mode: "never", version: "2.1.280", sdk: "0.112.1", runtime: "v26.3.0", stabilize: true, wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "native fallback billing", mode: "auto", version: "2.1.280", sdk: "0.112.1", runtime: "v26.3.0", omitBilling: true, wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "native current with legacy pair", mode: "auto", version: "2.1.321", sdk: "0.112.1", runtime: "v26.3.0", wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "unknown native incomplete tuple", mode: "auto", version: "2.1.322", sdk: "0.129.0", wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "synthetic newer native full tuple", mode: "auto", version: "2.1.322", sdk: "0.129.0", runtime: "v26.13.0", wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "reconstructed credential UA", mode: "always", version: "2.1.280", sdk: "0.112.1", runtime: "v26.3.0", customUA: "claude-cli/2.1.280 (external, cli)", stabilize: true, wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "configured known UA defaults", mode: "always", version: "2.1.280", sdk: "0.112.1", runtime: "v26.3.0", configuredUA: "claude-cli/2.1.280 (external, cli)", wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "reconstructed SDK entrypoint", mode: "always", version: "2.1.321", sdk: "0.128.0", runtime: "v26.12.1", customUA: "claude-cli/2.1.321 (external, sdk-cli)", wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1", wantEntrypoint: "sdk-cli"},
				{name: "native explicit SDK entrypoint", mode: "auto", version: "2.1.321", sdk: "0.128.0", runtime: "v26.12.1", customUA: "claude-cli/2.1.321 (external, sdk-cli)", wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1", wantEntrypoint: "sdk-cli"},
				{name: "native payload text override", mode: "never", version: "2.1.321", sdk: "0.128.0", runtime: "v26.12.1", rewriteMessage: true, wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
				{name: "reconstructed current baseline", mode: "always", version: "2.1.280", sdk: "0.112.1", runtime: "v26.3.0", stabilize: true, wantVersion: "2.1.321", wantSDK: "0.128.0", wantRuntime: "v26.12.1"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					payload := []byte(`{"model":"claude-opus-5","max_tokens":1024,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=` + tc.version + `.abc; cc_entrypoint=cli; cch=00000;"},{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[{"role":"user","content":"say ok"}],"metadata":{"user_id":"` + strings.ReplaceAll(claudeNativeHelperUserID, `"`, `\"`) + `"}}`)
					if tc.omitBilling {
						payload, _ = sjson.DeleteBytes(payload, "system.0")
					}
					headers := claudeNativeHelperHeaders(claudeNativeHelperCoreBetas)
					headers.Set("User-Agent", "claude-cli/"+tc.version+" (external, cli)")
					headers.Set("X-Stainless-Package-Version", tc.sdk)
					headers.Set("X-Stainless-Runtime-Version", tc.runtime)
					headers.Set("X-Stainless-OS", "Linux")
					headers.Set("X-Stainless-Arch", "x64")
					cfg := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{StabilizeDeviceProfile: &tc.stabilize, UserAgent: tc.configuredUA}}
					if tc.rewriteMessage {
						cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "claude-opus-5"}}, Params: map[string]any{"messages.0.content": "changed first user text with different samples"}}}
					}
					auth := &cliproxyauth.Auth{ID: uuid.NewString(), Attributes: map[string]string{"api_key": "sk-ant-oat-coherence-test", "cloak_mode": tc.mode}, Metadata: claudeOAuthTestMetadata()}
					if tc.customUA != "" {
						auth.Attributes["header:User-Agent"] = tc.customUA
					}
					var captured []byte
					transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
						body, errRead := io.ReadAll(request.Body)
						if errRead != nil {
							return nil, errRead
						}
						captured = body
						for name, want := range map[string]string{"X-Stainless-OS": "MacOS", "X-Stainless-Arch": "arm64", "X-Stainless-Runtime": "node", "X-Stainless-Lang": "js"} {
							if got := helps.HeaderValueCaseInsensitive(request.Header, name); got != want {
								t.Errorf("%s=%q, want %q", name, got, want)
							}
						}
						if got := helps.HeaderValueCaseInsensitive(request.Header, "User-Agent"); !strings.Contains(got, "claude-cli/"+tc.wantVersion+" ") {
							t.Errorf("UA=%s, want version %s", got, tc.wantVersion)
						}
						if got := helps.HeaderValueCaseInsensitive(request.Header, "X-Stainless-Package-Version"); got != tc.wantSDK {
							t.Errorf("SDK=%s, want %s", got, tc.wantSDK)
						}
						if got := helps.HeaderValueCaseInsensitive(request.Header, "X-Stainless-Runtime-Version"); got != tc.wantRuntime {
							t.Errorf("runtime=%s, want %s", got, tc.wantRuntime)
						}
						if path != "count_tokens" {
							billing := gjson.GetBytes(body, "system.0.text").String()
							if !strings.Contains(billing, "cc_version="+tc.wantVersion+".") {
								t.Errorf("billing=%s, want version %s", billing, tc.wantVersion)
							}
							wantEntrypoint := tc.wantEntrypoint
							if wantEntrypoint == "" {
								wantEntrypoint = "cli"
							}
							if !strings.Contains(billing, "cc_entrypoint="+wantEntrypoint+";") {
								t.Errorf("billing entrypoint=%s, want %s", billing, wantEntrypoint)
							}
							if tc.rewriteMessage {
								want := "cc_version=" + tc.wantVersion + "." + computeFingerprint(claudeBillingFingerprintMessageText(body), tc.wantVersion) + ";"
								if !strings.Contains(billing, want) {
									t.Errorf("billing must hash final text: %s; want %s", billing, want)
								}
							}
							signed, errSign := finalizeAnthropicMessagesBodyCCH(body, "")
							if errSign != nil {
								t.Fatal(errSign)
							}
							if !bytes.Equal(signed, body) {
								t.Error("CCH does not cover final outgoing body")
							}
						}
						contentType := "application/json"
						responseBody := `{"id":"msg_coherence","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`
						if path == "count_tokens" {
							responseBody = `{"input_tokens":1}`
						} else if path == "stream" {
							contentType = "text/event-stream"
							responseBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":" + responseBody + "}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
						}
						return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: request}, nil
					})
					ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
					executor := NewClaudeExecutor(cfg)
					request := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
					options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: headers}
					switch path {
					case "messages":
						if _, errExecute := executor.Execute(ctx, auth, request, options); errExecute != nil {
							t.Fatal(errExecute)
						}
					case "stream":
						result, errStream := executor.ExecuteStream(ctx, auth, request, options)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					case "count_tokens":
						if _, errCount := executor.CountTokens(ctx, auth, request, options); errCount != nil {
							t.Fatal(errCount)
						}
					}
					if captured == nil {
						t.Fatal("upstream not called")
					}
				})
			}
		})
	}
}

func TestClaudeReconstructedEnvironmentHeaders(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		native, gateway, override, wantEnvironment bool
	}{
		{name: "reconstructed official"},
		{name: "confirmed native", native: true, wantEnvironment: true},
		{name: "custom gateway", gateway: true, wantEnvironment: true},
		{name: "explicit operator", override: true, wantEnvironment: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint := "https://api.anthropic.com/v1/messages"
			if tc.gateway {
				endpoint = "https://gateway.example/v1/messages"
			}
			request, errRequest := http.NewRequest(http.MethodPost, endpoint, nil)
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			incoming := http.Header{"X-Client-App": {"third-party-app"}, "X-Claude-Remote-Container-Id": {"caller-container"}, "X-Claude-Remote-Session-Id": {"caller-remote-session"}, "X-Claude-Code-Agent-Id": {"agent-child"}, "X-Claude-Code-Parent-Agent-Id": {"agent-parent"}, "X-Anthropic-Additional-Protection": {"keep-protection"}}
			auth := &cliproxyauth.Auth{ID: uuid.NewString(), Attributes: map[string]string{"api_key": "sk-ant-oat-environment-test"}}
			if tc.override {
				for _, name := range []string{"X-Client-App", "X-Claude-Remote-Container-Id", "X-Claude-Remote-Session-Id"} {
					auth.Attributes["header:"+name] = incoming.Get(name)
				}
			}
			if errHeaders := applyClaudeHeadersWithNativeProfile(request, auth, "sk-ant-oat-environment-test", false, nil, []byte(`{"model":"claude-opus-5"}`), &config.Config{}, incoming, tc.native, false, claudeNativeHelperSessionID); errHeaders != nil {
				t.Fatal(errHeaders)
			}
			for _, name := range []string{"X-Client-App", "X-Claude-Remote-Container-Id", "X-Claude-Remote-Session-Id"} {
				if got := request.Header.Get(name); (got != "") != tc.wantEnvironment {
					t.Errorf("%s=%q, want present %t", name, got, tc.wantEnvironment)
				}
			}
			for _, name := range []string{"X-Claude-Code-Agent-Id", "X-Claude-Code-Parent-Agent-Id", "X-Anthropic-Additional-Protection"} {
				if request.Header.Get(name) != incoming.Get(name) {
					t.Errorf("lost semantic header %s", name)
				}
			}
		})
	}
}

func TestClaudeDefaultProfilePreservesCallerBilling(t *testing.T) {
	for _, path := range []string{"execute", "stream"} {
		for _, credential := range []string{"api-key", "delegated"} {
			for _, native := range []bool{false, true} {
				name := path + "/" + credential + "/unconfirmed"
				if native {
					name = path + "/" + credential + "/native"
				}
				t.Run(name, func(t *testing.T) {
					upstream := &midSystemUpstream{}
					auth := &cliproxyauth.Auth{ID: uuid.NewString(), Provider: "claude", Attributes: map[string]string{"cloak_mode": "never"}}
					if credential == "api-key" {
						auth.Attributes["api_key"] = "synthetic-caller-key"
					} else {
						auth.Metadata = map[string]any{"access_token": "synthetic-delegated-token"}
					}
					payload := []byte(`{"model":"claude-opus-5","max_tokens":1024,"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.280.caller; cc_entrypoint=sdk-cli; cch=abc12;"}],"messages":[{"role":"user","content":"caller text"}],"metadata":{"user_id":"` + strings.ReplaceAll(claudeNativeHelperUserID, `"`, `\"`) + `"}}`)
					headers := http.Header{"User-Agent": {"claude-cli/2.1.321 (external, cli)"}}
					if native {
						headers = claudeNativeHelperHeaders(claudeNativeHelperCoreBetas)
						headers.Set("User-Agent", "claude-cli/2.1.321 (external, cli)")
						headers.Set("X-Stainless-Package-Version", "0.128.0")
						headers.Set("X-Stainless-Runtime-Version", "v26.12.1")
					}
					if got := helps.DetectClaudeCodeRequest(headers, payload, false).Confirmed; got != native {
						t.Fatalf("fixture native=%t, want %t", got, native)
					}
					req := cliproxyexecutor.Request{Model: "claude-opus-5", Payload: payload}
					if err := runFinalThinkingExecutor(path, NewClaudeExecutor(&config.Config{}), upstream.context(t, headers), auth, req); err != nil {
						t.Fatal(err)
					}
					if !upstream.called {
						t.Fatal("upstream not called")
					}
					if got, want := gjson.GetBytes(upstream.body, "system.0.text").String(), gjson.GetBytes(payload, "system.0.text").String(); got != want {
						t.Fatalf("default profile changed caller billing: got %q, want %q", got, want)
					}
				})
			}
		}
	}
}

// The native TLS profile is fixed at the first-party CLI send boundary.
func TestClaudePinnedProfileScopeAndOverridePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint, token, fingerprint, mode string
		pinned                                   bool
	}{
		{name: "OAuth", endpoint: "https://api.anthropic.com/v1/messages", token: "sk-ant-oat-scope", pinned: true},
		{name: "API key CLI opt-in", endpoint: "https://api.anthropic.com/v1/messages", token: "key", fingerprint: "claude-code-cli", pinned: true},
		{name: "explicit cloak", endpoint: "https://api.anthropic.com/v1/messages", token: "key", mode: "always", pinned: true},
		{name: "caller-owned API key", endpoint: "https://api.anthropic.com/v1/messages", token: "key"},
		{name: "custom gateway", endpoint: "https://gateway.example/v1/messages", token: "sk-ant-oat-scope"},
		{name: "similarly named host", endpoint: "https://api.anthropic.com.example/v1/messages", token: "sk-ant-oat-scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, tc.endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			auth := &cliproxyauth.Auth{ID: uuid.NewString(), Attributes: map[string]string{
				"api_key": tc.token, "fingerprint_profile": tc.fingerprint, "cloak_mode": tc.mode,
				"header:User-Agent":                  "claude-cli/2.1.280 (external, sdk-cli, workload/cron)",
				"header:X-Stainless-Package-Version": "9.9.9", "header:X-Stainless-Runtime-Version": "v1.2.3",
				"header:X-Stainless-OS": "Linux", "header:X-Stainless-Arch": "x64",
				"header:X-Stainless-Runtime": "python", "header:X-Stainless-Lang": "python",
			}}
			cfg := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{UserAgent: "claude-cli/2.1.280 (external, cli)", OS: "Windows", Arch: "x64"}}
			if err := applyClaudeHeaders(req, auth, tc.token, false, nil, []byte(`{"model":"claude-sonnet-4-5"}`), cfg, nil, false); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"User-Agent": "claude-cli/2.1.280 (external, sdk-cli, workload/cron)", "X-Stainless-Package-Version": "9.9.9", "X-Stainless-Runtime-Version": "v1.2.3", "X-Stainless-OS": "Linux", "X-Stainless-Arch": "x64", "X-Stainless-Runtime": "python", "X-Stainless-Lang": "python"}
			if tc.pinned {
				want = map[string]string{"User-Agent": "claude-cli/2.1.321 (external, sdk-cli, workload/cron)", "X-Stainless-Package-Version": "0.128.0", "X-Stainless-Runtime-Version": "v26.12.1", "X-Stainless-OS": "MacOS", "X-Stainless-Arch": "arm64", "X-Stainless-Runtime": "node", "X-Stainless-Lang": "js"}
			}
			for key, value := range want {
				if got := req.Header.Get(key); got != value {
					t.Errorf("%s=%q, want %q", key, got, value)
				}
			}
		})
	}
}
