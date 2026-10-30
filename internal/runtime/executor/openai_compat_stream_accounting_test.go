package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

type compatStreamAccountingCapture struct {
	alias   string
	records chan usage.Record
}

func (p *compatStreamAccountingCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Alias == p.alias {
		p.records <- record
	}
}

func TestOpenAICompatStreamPreservesUsageOnFailureAndMultilineFrames(t *testing.T) {
	for _, scenario := range []string{"multiline-success", "explicit-error", "read-error"} {
		t.Run(scenario, func(t *testing.T) {
			event := "data: " + `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}` + "\n\n"
			if scenario == "multiline-success" {
				event = "data: " + `{"id":"chatcmpl-1","choices":[],"usage":` + "\n" + "data: " + `{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}` + "\n\ndata: [DONE]\n\n"
			} else if scenario == "explicit-error" {
				event += "data: " + `{"error":{"message":"fixture failure","type":"server_error","code":502}}` + "\n\n"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if scenario == "read-error" {
					w.Header().Set("Content-Length", fmt.Sprint(len(event)+100))
				}
				if _, errWrite := fmt.Fprint(w, event); errWrite != nil {
					t.Error(errWrite)
				}
			}))
			defer server.Close()
			capture := &compatStreamAccountingCapture{alias: t.Name(), records: make(chan usage.Record, 4)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			ctx := usage.WithRequestedModelAlias(context.Background(), t.Name())
			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			res, err := exec.ExecuteStream(ctx, &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: map[string]string{"base_url": server.URL, "api_key": "fixture"}}, cliproxyexecutor.Request{Model: "fixture-model", Payload: []byte(`{"model":"fixture-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var streamErr error
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			wantFailure := scenario != "multiline-success"
			if (streamErr != nil) != wantFailure {
				t.Fatalf("stream error = %v, want failure=%v", streamErr, wantFailure)
			}
			select {
			case record := <-capture.records:
				if record.Failed != wantFailure || record.Detail.InputTokens != 12 || record.Detail.OutputTokens != 8 || record.Detail.TotalTokens != 20 {
					t.Fatalf("failed=%v usage=%+v, want failure=%v and 12/8/20 tokens", record.Failed, record.Detail, wantFailure)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("usage record not delivered")
			}
		})
	}
}
