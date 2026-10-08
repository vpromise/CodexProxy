package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorPreservesAttachments(t *testing.T) {
	for _, tc := range []struct {
		name                                   string
		format                                 sdktranslator.Format
		payload, wantType, wantPath, wantValue string
	}{
		{"claude_document", sdktranslator.FormatClaude, `{"model":"test","messages":[{"role":"user","content":[{"type":"document","filename":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}}]}]}`, "file", "file.file_data", "data:application/pdf;base64,JVBERi0="},
		{"responses_file", sdktranslator.FormatOpenAIResponse, `{"model":"test","input":[{"role":"user","content":[{"type":"input_file","file_id":"file-fixture"}]}]}`, "file", "file.file_id", "file-fixture"},
		{"responses_audio", sdktranslator.FormatOpenAIResponse, `{"model":"test","input":[{"role":"user","content":[{"type":"input_audio","data":"YXVkaW8=","format":"wav"}]}]}`, "input_audio", "input_audio.data", "YXVkaW8="},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, stream), func(t *testing.T) {
				calls := 0
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", eofFixtureTransport(func(req *http.Request) (*http.Response, error) {
					calls++
					body, errRead := io.ReadAll(req.Body)
					if errRead != nil {
						return nil, errRead
					}
					part := gjson.GetBytes(body, "messages.0.content.0")
					if part.Get("type").String() != tc.wantType || part.Get(tc.wantPath).String() != tc.wantValue {
						t.Errorf("attachment missing on wire: %s", body)
					}
					response := `{"id":"fixture","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
					contentType := "application/json"
					if stream {
						response = "data: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
						contentType = "text/event-stream"
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				}))
				ex := NewOpenAICompatExecutor("custom-compat", &config.Config{})
				auth := &cliproxyauth.Auth{Provider: "custom-compat", Attributes: map[string]string{"base_url": "http://fixture.invalid/v1", "api_key": "fixture"}}
				payload := []byte(tc.payload)
				req := cliproxyexecutor.Request{Model: "test", Payload: payload}
				opts := cliproxyexecutor.Options{SourceFormat: tc.format, ResponseFormat: sdktranslator.FormatOpenAI, OriginalRequest: payload, Stream: stream}
				if stream {
					result, errStream := ex.ExecuteStream(ctx, auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else if _, errExecute := ex.Execute(ctx, auth, req, opts); errExecute != nil {
					t.Fatal(errExecute)
				}
				if calls != 1 {
					t.Fatalf("upstream calls = %d, want 1", calls)
				}
			})
		}
	}
}
