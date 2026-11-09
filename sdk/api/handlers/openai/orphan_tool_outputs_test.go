package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesSanitizesOrphanToolOutputsThroughHTTPAndCompact(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const modelID = "test-responses-orphan-tool-output-model"
	authID := "test-responses-orphan-tool-output-auth"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	coreManager := coreauth.NewManager(nil, nil, nil)
	executor := &responsesMultiAgentCaptureExecutor{}
	coreManager.RegisterExecutor(executor)
	if _, errRegister := coreManager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	baseHandler := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, coreManager)
	responsesHandler := NewOpenAIResponsesAPIHandler(baseHandler)

	router := gin.New()
	router.POST("/v1/responses", responsesHandler.Responses)
	router.POST("/v1/responses/compact", responsesHandler.Compact)

	payload := fmt.Sprintf(`{
		"model": %q,
		"stream": false,
		"input": [
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},
			{"type":"function_call_output","id":"fco_1","name":"codex_apps_open_page","namespace":"agent","output":"{\"page_id\":null}"},
			{"type":"custom_tool_call_output","id":"fco_2","name":"custom_ctx","output":[{"type":"input_text","text":"part one"},{"type":"input_text","text":"part two"},{"type":"input_image","image_url":"https://example.com/page.png"}]},
			{"type":"function_call","call_id":"call_ok","name":"shell","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_ok","output":"ok"},
			{"type":"function_call_output","call_id":" ","output":"blank id"},
			{"type":"function_call_output","id":"fco_3","name":"create_thread","namespace":"codex_app","output":"<codex_delegation>task</codex_delegation>"},
			{"type":"function_call","call_id":"call_pair","name":"shell","arguments":"{}"},
			{"type":"function_call_output","output":"paired without call_id"}
		]
	}`, modelID)

	for idx, path := range []string{"/v1/responses", "/v1/responses/compact", "/v1/responses"} {
		body := payload
		if idx == 2 {
			body = strings.Replace(payload, `"stream": false`, `"stream": true`, 1)
		}
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200; body=%s", path, rec.Code, rec.Body.String())
		}
		payloads := executor.Payloads()
		if len(payloads) != idx+1 {
			t.Fatalf("%s captured payload count = %d, want %d", path, len(payloads), idx+1)
		}
		input := gjson.GetBytes(payloads[idx], "input").Array()
		if len(input) != 9 {
			t.Fatalf("%s input len = %d, want 9", path, len(input))
		}
		if got := input[1].Get("type").String(); got != "message" {
			t.Fatalf("%s input[1].type = %q, want message", path, got)
		}
		want1 := "Tool output from codex_apps_open_page:\n{\"page_id\":null}"
		if got := input[1].Get("content.0.text").String(); got != want1 {
			t.Fatalf("%s input[1].content.0.text = %q, want %q", path, got, want1)
		}
		if got := input[2].Get("type").String(); got != "message" {
			t.Fatalf("%s input[2].type = %q, want message", path, got)
		}
		want2 := "Tool output from custom_ctx:\npart one\npart two"
		if got := input[2].Get("content.0.text").String(); got != want2 {
			t.Fatalf("%s input[2].content.0.text = %q, want %q", path, got, want2)
		}
		if got := input[2].Get("content.1.type").String(); got != "input_image" {
			t.Fatalf("%s input[2].content.1.type = %q, want input_image", path, got)
		}
		if got := input[2].Get("content.1.image_url").String(); got != "https://example.com/page.png" {
			t.Fatalf("%s input[2].content.1.image_url = %q, want https://example.com/page.png", path, got)
		}
		if got := input[4].Get("type").String(); got != "function_call_output" {
			t.Fatalf("%s input[4].type = %q, want function_call_output", path, got)
		}
		if got := input[5].Get("type").String(); got != "message" {
			t.Fatalf("%s input[5].type = %q, want message", path, got)
		}
		want5 := "Tool output from tool:\nblank id"
		if got := input[5].Get("content.0.text").String(); got != want5 {
			t.Fatalf("%s input[5].content.0.text = %q, want %q", path, got, want5)
		}
		if got := input[6].Get("type").String(); got != "function_call_output" {
			t.Fatalf("%s input[6].type = %q, want function_call_output", path, got)
		}
		if got := input[8].Get("type").String(); got != "function_call_output" {
			t.Fatalf("%s input[8].type = %q, want function_call_output", path, got)
		}
		if got := input[8].Get("call_id").String(); got != "call_pair" {
			t.Fatalf("%s input[8].call_id = %q, want call_pair", path, got)
		}
	}
}
