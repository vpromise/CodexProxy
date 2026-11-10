package helps

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"net/http/httptest"
	"testing"
)

func TestClaudeDefaultSoftwareTuple2321(t *testing.T) {
	for _, cfg := range []*config.Config{nil, {}} {
		req := httptest.NewRequest("POST", "https://example.invalid/messages", nil)
		ApplyClaudeDefaultDeviceProfileHeaders(req, cfg)
		for key, want := range map[string]string{"User-Agent": "claude-cli/2.1.321 (external, cli)", "X-Stainless-Package-Version": "0.128.0", "X-Stainless-Runtime-Version": "v26.12.1", "X-Stainless-Os": "MacOS", "X-Stainless-Arch": "arm64"} {
			if got := req.Header.Get(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		if got := DefaultClaudeVersion(cfg); got != "2.1.321" {
			t.Errorf("billing version = %q", got)
		}
	}
	cfg := &config.Config{ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{UserAgent: "claude-cli/2.1.280 (external, cli)", PackageVersion: "custom-sdk", RuntimeVersion: "custom-runtime"}}
	req := httptest.NewRequest("POST", "https://example.invalid/messages", nil)
	ApplyClaudeDefaultDeviceProfileHeaders(req, cfg)
	if req.Header.Get("User-Agent") != cfg.ClaudeHeaderDefaults.UserAgent || req.Header.Get("X-Stainless-Package-Version") != "custom-sdk" || req.Header.Get("X-Stainless-Runtime-Version") != "custom-runtime" {
		t.Fatal("explicit operator tuple overwritten")
	}
}

func TestClaudeBackgroundSessionKeepsNativeGuards(t *testing.T) {
	for _, count := range []bool{false, true} {
		for _, app := range []string{"cli", "cli-bg", "cli-other", "CLI-BG", "web", ""} {
			headers := confirmedClaudeCodeHeaders()
			headers.Set("User-Agent", "claude-cli/2.1.258 (external, cli, workload/cron)")
			headers.Set("X-App", app)
			payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
			if count {
				payload = []byte(`{"messages":[]}`)
			}
			detection := DetectClaudeCodeRequest(headers, payload, count)
			want := app == "cli" || app == "cli-bg"
			if detection.Confirmed != want || detection.XAppCLI != want {
				t.Errorf("count=%v app=%q detection=%+v", count, app, detection)
			}
			headers.Del("Anthropic-Beta")
			if DetectClaudeCodeRequest(headers, payload, count).Confirmed {
				t.Fatal("missing beta bypassed native guard")
			}
		}
	}
	headers := measuredClaudeCodeHelperHeaders(claudeCodeHelperBetaProfile(true))
	headers.Set("X-App", "cli-bg")
	if d := DetectClaudeCodeRequest(headers, measuredClaudeCodeTitleHelperPayload(), false); !d.Confirmed || !d.HelperProfile {
		t.Fatalf("background helper=%+v", d)
	}
}

func TestClaudeMeasuredLegacyHelperTupleRemainsIndependent(t *testing.T) {
	for _, version := range []string{"2.1.258", "2.1.280"} {
		headers := measuredClaudeCodeHelperHeaders(claudeCode258HelperBetaProfile(true, false))
		headers.Set("User-Agent", "claude-cli/"+version+" (external, cli)")
		headers.Set("X-Stainless-Package-Version", "0.112.1")
		headers.Set("X-Stainless-Runtime-Version", "v26.3.0")
		headers.Set("X-App", "cli-bg")
		if d := DetectClaudeCodeRequest(headers, measuredClaudeCodeTitleHelperPayload(), false); !d.HelperProfile {
			t.Fatalf("lost measured legacy tuple: %+v", d)
		}
		request := httptest.NewRequest("POST", "https://example.invalid/messages", nil)
		ApplyClaudeLegacyDeviceHeaders(request, headers, nil, true)
		if request.Header.Get("User-Agent") != headers.Get("User-Agent") || request.Header.Get("X-Stainless-Package-Version") != "0.112.1" || request.Header.Get("X-Stainless-Runtime-Version") != "v26.3.0" {
			t.Fatal("rewrote accepted native legacy pair")
		}
		headers.Set("X-Stainless-Package-Version", "0.999.0")
		if d := DetectClaudeCodeRequest(headers, measuredClaudeCodeTitleHelperPayload(), false); d.HelperProfile {
			t.Fatal("accepted mixed helper tuple")
		}
		request = httptest.NewRequest("POST", "https://example.invalid/messages", nil)
		ApplyClaudeLegacyDeviceHeaders(request, headers, nil, true)
		if request.Header.Get("X-Stainless-Package-Version") != "0.128.0" || request.Header.Get("X-Stainless-Runtime-Version") != "v26.12.1" {
			t.Fatal("did not pin unknown native SDK pair")
		}
	}
}
