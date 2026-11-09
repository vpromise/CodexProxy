package executor

import (
	"context"
	"fmt"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClaudeCloakedTopLevelCacheControl_Issue7366(t *testing.T) {
	newCloakedOAuthEnv := func(t *testing.T) (context.Context, *cliproxyauth.Auth, *ClaudeExecutor, *[][]byte) {
		t.Helper()
		var capturedBodies [][]byte
		call := 0
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			capturedBodies = append(capturedBodies, body)
			call++
			response := fmt.Sprintf(`{"id":"msg_%d","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":2}}`, call)
			header := make(http.Header)
			header.Set("Content-Type", "application/json")
			header.Set("request-id", fmt.Sprintf("req_issue7366_%d", call))
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(response)),
				Request:    req,
			}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		auth := &cliproxyauth.Auth{
			ID:         "test-issue7366-oauth",
			Attributes: map[string]string{"api_key": "sk-ant-oat-test-issue7366-key"},
			Metadata: map[string]any{
				"account_uuid": "11111111-2222-4333-8444-555555555555",
				claudeauth.ClaudeDeviceIDsMetadataKey: []string{
					"0000000000000000000000000000000000000000000000000000000000000001",
				},
			},
		}
		return ctx, auth, NewClaudeExecutor(&config.Config{}), &capturedBodies
	}

	t.Run("Error1_SingleTurnBareTopLevelCacheControlNoTTLMismatch", func(t *testing.T) {
		ctx, auth, exec, captured := newCloakedOAuthEnv(t)
		payload := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"You are a helpful assistant.","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":[{"type":"text","text":"What time is it?"}]}]}`)
		opts := cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatClaude,
			Headers:      http.Header{"Anthropic-Version": []string{"2023-06-01"}},
		}
		if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if len(*captured) != 1 {
			t.Fatalf("captured %d bodies, want 1", len(*captured))
		}
		seenBody := (*captured)[0]
		if gjson.GetBytes(seenBody, "cache_control").Exists() {
			t.Fatalf("expected top-level cache_control to be materialized and removed on cloaked request, got body: %s", seenBody)
		}
		if got := gjson.GetBytes(seenBody, "system.1.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("system.1.cache_control.ttl = %q, want 1h: %s", got, seenBody)
		}
		if got := gjson.GetBytes(seenBody, "messages.0.content.1.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("messages.0.content.1.cache_control.ttl = %q, want 1h: %s", got, seenBody)
		}
		if got := gjson.GetBytes(seenBody, "messages.1.content.0.cache_control.ttl").String(); got != "1h" {
			t.Fatalf("messages.1.content.0.cache_control.ttl = %q, want 1h: %s", got, seenBody)
		}
	})

	t.Run("Error2_ToolResultTurnWith1hTopLevelCacheControlStaysWithin4Breakpoints", func(t *testing.T) {
		ctx, auth, exec, captured := newCloakedOAuthEnv(t)
		payload := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"cache_control":{"type":"ephemeral","ttl":"1h"},"system":[{"type":"text","text":"You are a helpful assistant.","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"get_time","description":"Returns the current time.","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":[{"type":"text","text":"What time is it?"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"get_time","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"12:00"}]}]}`)
		opts := cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatClaude,
			Headers:      http.Header{"Anthropic-Version": []string{"2023-06-01"}},
		}
		if _, err := exec.Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, opts); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if len(*captured) != 1 {
			t.Fatalf("captured %d bodies, want 1", len(*captured))
		}
		seenBody := (*captured)[0]
		if gjson.GetBytes(seenBody, "cache_control").Exists() {
			t.Fatalf("expected top-level cache_control to be materialized onto last cacheable block and removed, got body: %s", seenBody)
		}
		if total := countCacheControls(seenBody); total != 4 {
			t.Fatalf("total cache_control count = %d, want 4: %s", total, seenBody)
		}
		for _, path := range []string{
			"system.1.cache_control.ttl",
			"messages.0.content.1.cache_control.ttl",
			"messages.1.content.0.cache_control.ttl",
			"messages.3.content.0.cache_control.ttl",
		} {
			if got := gjson.GetBytes(seenBody, path).String(); got != "1h" {
				t.Fatalf("%s = %q, want 1h: %s", path, got, seenBody)
			}
		}
	})

	t.Run("StreamPath_MaterializesTopLevelCacheControlAndStaysWithin4Breakpoints", func(t *testing.T) {
		var seenBody []byte
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			var errRead error
			seenBody, errRead = io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatal(errRead)
			}
			header := make(http.Header)
			header.Set("Content-Type", "text/event-stream")
			sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_s1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     header,
				Body:       io.NopCloser(strings.NewReader(sse)),
				Request:    req,
			}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		auth := &cliproxyauth.Auth{
			ID:         "test-issue7366-stream-oauth",
			Attributes: map[string]string{"api_key": "sk-ant-oat-test-issue7366-stream-key"},
			Metadata: map[string]any{
				"account_uuid": "11111111-2222-4333-8444-555555555555",
				claudeauth.ClaudeDeviceIDsMetadataKey: []string{
					"0000000000000000000000000000000000000000000000000000000000000001",
				},
			},
		}
		exec := NewClaudeExecutor(&config.Config{})
		payload := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"stream":true,"cache_control":{"type":"ephemeral","ttl":"1h"},"system":[{"type":"text","text":"You are a helpful assistant.","cache_control":{"type":"ephemeral","ttl":"1h"}}],"tools":[{"name":"get_time","description":"Returns the current time.","input_schema":{"type":"object","properties":{}}}],"messages":[{"role":"user","content":[{"type":"text","text":"What time is it?"}]},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_01","name":"get_time","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":"12:00"}]}]}`)
		res, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatClaude,
			Headers:      http.Header{"Anthropic-Version": []string{"2023-06-01"}},
		})
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		if res != nil && res.Chunks != nil {
			for range res.Chunks {
			}
		}
		if gjson.GetBytes(seenBody, "cache_control").Exists() {
			t.Fatalf("expected top-level cache_control to be removed in stream path, got: %s", seenBody)
		}
		if total := countCacheControls(seenBody); total != 4 {
			t.Fatalf("total cache_control count = %d, want 4: %s", total, seenBody)
		}
	})

	t.Run("DirectAPIKeyPassthroughPreservesTopLevelCacheControlWithoutSyntheticBlocks", func(t *testing.T) {
		var seenBody []byte
		transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seenBody, _ = io.ReadAll(req.Body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"msg_direct","type":"message","model":"claude-opus-5-5","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)),
				Request:    req,
			}, nil
		})
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		auth := &cliproxyauth.Auth{
			Attributes: map[string]string{
				"api_key":  "sk-ant-api03-direct-top-cc-test",
				"base_url": "https://api.anthropic.com",
			},
		}
		payload := []byte(`{"model":"claude-opus-5-5","max_tokens":32,"cache_control":{"type":"ephemeral"},"system":[{"type":"text","text":"caller system"}],"messages":[{"role":"user","content":"hello"}]}`)
		if _, err := NewClaudeExecutor(&config.Config{}).Execute(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatClaude,
			Headers:      http.Header{"User-Agent": {"custom-sdk/1.0"}},
		}); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		if got := gjson.GetBytes(seenBody, "cache_control.type").String(); got != "ephemeral" {
			t.Fatalf("direct API key top-level cache_control.type = %q, want ephemeral: %s", got, seenBody)
		}
		if gjson.GetBytes(seenBody, "system.0.cache_control").Exists() || gjson.GetBytes(seenBody, "messages.0.content.0.cache_control").Exists() {
			t.Fatalf("direct API key request with top-level cache_control must not gain synthetic block breakpoints: %s", seenBody)
		}
	})

	t.Run("HelpersRespectAndMaterializeTopLevelCacheControl", func(t *testing.T) {
		withScope := []byte(`{"cache_control":{"type":"ephemeral","scope":"global"},"system":"Sys","messages":[{"role":"user","content":"Hello"}]}`)
		ensured := ensureCacheControl(withScope)
		if gjson.GetBytes(ensured, "cache_control").Exists() {
			t.Fatalf("ensureCacheControl should remove materialized top-level cache_control: %s", ensured)
		}
		if got := gjson.GetBytes(ensured, "messages.0.content.0.cache_control.scope").String(); got != "global" {
			t.Fatalf("messages.0.content.0.cache_control.scope = %q, want global: %s", got, ensured)
		}

		// When top-level cache_control remains (e.g. direct call to enforceCacheControlLimit / upgrade / normalize / strip),
		// helpers still account for it.
		fiveMarkers := []byte(`{
			"cache_control":{"type":"ephemeral"},
			"system":[
				{"type":"text","text":"s0","cache_control":{"type":"ephemeral"}},
				{"type":"text","text":"s1","cache_control":{"type":"ephemeral"}}
			],
			"messages":[{
				"role":"user",
				"content":[
					{"type":"text","text":"m0","cache_control":{"type":"ephemeral"}},
					{"type":"text","text":"m1","cache_control":{"type":"ephemeral"}}
				]
			}]
		}`)
		limited := enforceCacheControlLimit(fiveMarkers, 4)
		if total := countCacheControls(limited); total != 4 {
			t.Fatalf("countCacheControls(limited) = %d, want 4: %s", total, limited)
		}
		upgraded := upgradeClaudeCacheControlTTL(limited, claudeCacheControlTTL1h)
		if got := gjson.GetBytes(upgraded, "cache_control.ttl").String(); got != "1h" {
			t.Fatalf("top-level cache_control.ttl after upgrade = %q, want 1h: %s", got, upgraded)
		}
		stripped := stripClaudeCacheControlTTL(upgraded)
		if gjson.GetBytes(stripped, "cache_control.ttl").Exists() {
			t.Fatalf("top-level cache_control.ttl should be stripped: %s", stripped)
		}
		mixedOrder := []byte(`{
			"cache_control":{"type":"ephemeral","ttl":"1h"},
			"messages":[{"role":"user","content":[{"type":"text","text":"m0","cache_control":{"type":"ephemeral"}}]}]
		}`)
		normalized := normalizeCacheControlTTL(mixedOrder)
		if gjson.GetBytes(normalized, "cache_control.ttl").Exists() {
			t.Fatalf("normalizeCacheControlTTL must downgrade top-level 1h TTL after a 5m block: %s", normalized)
		}
	})
}
