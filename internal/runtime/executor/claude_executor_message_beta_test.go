package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestApplyClaudeHeaders_MessageFeatureBetasFollowCaller(t *testing.T) {
	const perTurn = "per-turn-control-2026-07-01"
	const toolChanges = "mid-conversation-tool-changes-2026-07-01"
	const unknown = "unknown-message-feature-2099-01-01"
	body := []byte(`{"model":"claude-fable-5-1","messages":[{"role":"user","content":"hello"},{"role":"system","content":[],"output_config":{"effort":"xhigh"}}]}`)
	tests := []struct {
		name      string
		header    []string
		extra     []string
		confirmed bool
		want      []string
	}{
		{name: "not inferred from body", extra: []string{unknown}},
		{name: "per turn only", header: []string{perTurn + "," + unknown}, want: []string{perTurn}},
		{name: "tool changes only", header: []string{toolChanges}, want: []string{toolChanges}},
		{name: "multiple header values", header: []string{perTurn, toolChanges}, want: []string{perTurn, toolChanges}},
		{name: "body betas", extra: []string{perTurn, toolChanges, unknown}, want: []string{perTurn, toolChanges}},
		{name: "mixed sources deduplicated", header: []string{perTurn + ", " + toolChanges}, extra: []string{toolChanges, perTurn, unknown}, want: []string{perTurn, toolChanges}},
		{name: "native profile keeps body betas", confirmed: true, extra: []string{perTurn, toolChanges, unknown}, want: []string{perTurn, toolChanges}},
	}
	for _, mode := range []string{"messages", "stream", "count_tokens"} {
		for _, tt := range tests {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				incoming := http.Header{"anthropic-beta": append([]string{claudeCodeBeta + "," + claudeMidConvSystemBeta}, tt.header...)}
				req := newClaudeHeaderTestRequest(t, nil)
				if mode == "count_tokens" {
					req.URL.Path += "/count_tokens"
				}
				if errHeaders := applyClaudeHeaders(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey,
					mode == "stream", tt.extra, body, nil, incoming, tt.confirmed); errHeaders != nil {
					t.Fatalf("applyClaudeHeaders() error = %v", errHeaders)
				}
				got := req.Header.Get("Anthropic-Beta")
				counts := make(map[string]int)
				for _, beta := range strings.Split(got, ",") {
					counts[strings.TrimSpace(beta)]++
				}
				for _, beta := range []string{perTurn, toolChanges} {
					want := 0
					for _, requested := range tt.want {
						if requested == beta {
							want = 1
						}
					}
					if counts[beta] != want {
						t.Errorf("beta %s appears %d times, want %d; header = %q", beta, counts[beta], want, got)
					}
				}
				if counts[unknown] != 0 {
					t.Errorf("unknown beta reached reconstructed upstream header: %q", got)
				}
				if mode != "count_tokens" && !tt.confirmed && len(tt.want) > 0 {
					wantOrder := claudeMidConvSystemBeta + "," + strings.Join(tt.want, ",") + ","
					if !strings.Contains(got, wantOrder) {
						t.Errorf("header = %q, want feature betas in observed order after mid-conversation-system: %q", got, wantOrder)
					}
				}
				if !tt.confirmed && req.Header.Get("User-Agent") != "claude-cli/2.1.258 (external, cli)" {
					t.Errorf("User-Agent = %q, want the unchanged 2.1.258 baseline", req.Header.Get("User-Agent"))
				}
			})
		}
	}
}

// Intercept the transport so the real Anthropic header policy runs without network calls.
func TestClaudeExecutor_MessageFeatureBetasPreserveEffortControl(t *testing.T) {
	const perTurn = "per-turn-control-2026-07-01"
	const toolChanges = "mid-conversation-tool-changes-2026-07-01"
	const control = `{"role":"system","content":[],"output_config":{"effort":"xhigh"}}`
	for _, mode := range []string{"messages", "stream", "count_tokens"} {
		t.Run(mode, func(t *testing.T) {
			var upstreamBody []byte
			var upstreamHeaders http.Header
			var upstreamPath string
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				var errRead error
				upstreamBody, errRead = io.ReadAll(req.Body)
				if errRead != nil {
					return nil, errRead
				}
				upstreamHeaders = req.Header.Clone()
				upstreamPath = req.URL.Path
				if req.URL.Host != "api.anthropic.com" {
					t.Errorf("upstream host = %q, want the direct Anthropic policy", req.URL.Host)
				}
				contentType := "application/json"
				responseBody := `{"id":"msg_feature_beta","type":"message","role":"assistant","model":"claude-fable-5-1","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
				if mode == "count_tokens" {
					responseBody = `{"input_tokens":7}`
				} else if mode == "stream" {
					contentType = "text/event-stream"
					responseBody = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_feature_beta\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-fable-5-1\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n" +
						"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
						"data: {\"type\":\"message_stop\"}\n\n"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: req}, nil
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
			auth := &cliproxyauth.Auth{ID: t.Name(), Attributes: map[string]string{"api_key": "sk-ant-oat-message-features"}, Metadata: claudeOAuthTestMetadata()}
			payload := []byte(`{"model":"claude-fable-5-1","max_tokens":1024,"thinking":{"type":"adaptive"},"output_config":{"effort":"xhigh"},"messages":[{"role":"user","content":"hello"},` + control + `,{"role":"user","content":"continue"}],"betas":["` + toolChanges + `"]}`)
			req := cliproxyexecutor.Request{Model: "claude-fable-5-1", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: http.Header{
				"User-Agent":     {"claude-cli/2.1.267 (external, cli)"},
				"X-App":          {"cli"},
				"Anthropic-Beta": {claudeCodeBeta + "," + claudeMidConvSystemBeta + "," + perTurn},
			}}
			executor := NewClaudeExecutor(&config.Config{})
			switch mode {
			case "messages":
				if _, errExecute := executor.Execute(ctx, auth, req, opts); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
			case "stream":
				result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
				}
			case "count_tokens":
				if _, errCount := executor.CountTokens(ctx, auth, req, opts); errCount != nil {
					t.Fatalf("CountTokens() error = %v", errCount)
				}
			}
			wantPath := "/v1/messages"
			if mode == "count_tokens" {
				wantPath += "/count_tokens"
			}
			if upstreamPath != wantPath {
				t.Fatalf("upstream path = %q, want %q", upstreamPath, wantPath)
			}
			upstreamBetas := strings.Join(helps.HeaderValuesCaseInsensitive(upstreamHeaders, "Anthropic-Beta"), ",")
			for _, beta := range []string{perTurn, toolChanges} {
				if !strings.Contains(upstreamBetas, beta) {
					t.Errorf("upstream header is missing %s: %q", beta, upstreamBetas)
				}
			}
			foundControl := 0
			for _, message := range gjson.GetBytes(upstreamBody, "messages").Array() {
				if message.Get("output_config").Exists() {
					foundControl++
					if message.Raw != control {
						t.Errorf("effort-control message changed: got %s, want %s", message.Raw, control)
					}
				}
			}
			if foundControl != 1 {
				t.Errorf("upstream has %d effort-control messages, want 1", foundControl)
			}
			if gjson.GetBytes(upstreamBody, "betas").Exists() {
				t.Error("body betas were not lifted to the header")
			}
			if got := upstreamHeaders.Get("User-Agent"); got != "claude-cli/2.1.258 (external, cli)" {
				t.Errorf("User-Agent = %q, want the unchanged 2.1.258 baseline", got)
			}
		})
	}
}
