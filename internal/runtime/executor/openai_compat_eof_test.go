package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type eofFixtureTransport func(*http.Request) (*http.Response, error)

func (f eofFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type eofFixtureErrorReader struct{}

func (eofFixtureErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("synthetic read failure")
}

func TestOpenAICompatResponsesCleanEOF(t *testing.T) {
	text := "data: {\"id\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":null}]}\n\n"
	finish := "data: {\"id\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	usage := "data: {\"id\":\"chat-test\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1,\"total_tokens\":4}}\n\n"
	tool := "data: {\"id\":\"chat-test\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_one\",\"type\":\"function\",\"function\":{\"name\":\"lookup\",\"arguments\":\"{}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"
	for _, test := range []struct {
		name, body           string
		readError, wantError bool
		terminal             string
	}{
		{"text_and_late_usage", text + finish + usage, false, false, "response.completed"},
		{"tool_call", tool + usage, false, false, "response.completed"},
		{"length", text + strings.ReplaceAll(finish, "stop", "length"), false, false, "response.incomplete"},
		{"explicit_done", text + finish + usage + "data: [DONE]\n\n", false, false, "response.completed"},
		{"no_finish", text, false, true, ""},
		{"no_output", finish, false, true, ""},
		{"read_failure", text + finish, true, true, ""},
		{"explicit_error_after_finish", text + finish + "event: error\ndata: {\"error\":{\"message\":\"rejected\"}}\n\n", false, true, ""},
		{"malformed_after_finish", text + finish + "data: {\"incomplete\":\n\n", false, true, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ex := NewOpenAICompatExecutor("custom-compat", &config.Config{})
			auth := &cliproxyauth.Auth{Provider: "custom-compat", Attributes: map[string]string{"base_url": "http://fixture.invalid/v1", "api_key": "fixture"}}
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", eofFixtureTransport(func(req *http.Request) (*http.Response, error) {
				var body io.Reader = strings.NewReader(test.body)
				if test.readError {
					body = io.MultiReader(body, eofFixtureErrorReader{})
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(body), Request: req}, nil
			}))
			request := []byte(`{"model":"test","input":[{"role":"user","content":"hello"}],"stream":true}`)
			result, errStream := ex.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "test", Payload: request}, cliproxyexecutor.Options{SourceFormat: translator.FormatOpenAIResponse, ResponseFormat: translator.FormatOpenAIResponse, OriginalRequest: request, Stream: true})
			if errStream != nil {
				t.Fatal(errStream)
			}
			var output bytes.Buffer
			var streamErr error
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
				output.Write(chunk.Payload)
			}
			if (streamErr != nil) != test.wantError {
				t.Fatalf("error = %v, wantError = %v; output=%s", streamErr, test.wantError, output.String())
			}
			if test.terminal == "" {
				if strings.Contains(output.String(), `"type":"response.completed"`) || strings.Contains(output.String(), `"type":"response.incomplete"`) {
					t.Fatalf("false completion: %s", output.String())
				}
			} else if count := strings.Count(output.String(), `"type":"`+test.terminal+`"`); count != 1 {
				t.Fatalf("terminal count = %d: %s", count, output.String())
			}
			if test.name == "text_and_late_usage" && (!strings.Contains(output.String(), `"input_tokens":3`) || !strings.Contains(output.String(), `"output_tokens":1`)) {
				t.Fatalf("late usage missing: %s", output.String())
			}
		})
	}
}
