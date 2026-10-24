package pluginhost

import (
	"context"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestExecutorAdapterCountTokensTranslatesAcrossFormats(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-format-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"usage":{"prompt_tokens":17}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":17}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsPayloadWhenFormatsMatch(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-same-format-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":9}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatClaude}, []sdktranslator.Format{sdktranslator.FormatClaude})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":9}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want %s", got, want)
	}
}

func TestExecutorAdapterCountTokensFallsBackWhenCountMissing(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-no-count-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"unexpected":true}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatOpenAI}, []sdktranslator.Format{sdktranslator.FormatOpenAI})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if len(response.Payload) == 0 {
		t.Fatal("CountTokens() returned an empty payload when no count was present")
	}
}

func TestExecutorAdapterCountTokensRecognizesNativeCodexCount(t *testing.T) {
	adapter := newCurrentExecutorAdapterForTest(New(), "count-codex-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"response":{"usage":{"input_tokens":11,"output_tokens":0,"total_tokens":11}}}`)}, nil
		},
	}, []sdktranslator.Format{sdktranslator.FormatCodex}, []sdktranslator.Format{sdktranslator.FormatCodex})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "claude-model",
		Format:  sdktranslator.FormatClaude,
		Payload: []byte(`{"model":"claude-model","max_tokens":32,"messages":[{"role":"user","content":"hello"}]}`),
	}, coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":11}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsResponseTranslatorWithoutTokenCountHandler(t *testing.T) {
	pluginFormat := sdktranslator.Format("count-plugin-output-custom")
	clientFormat := sdktranslator.Format("count-client-output-custom")
	sdktranslator.Register(clientFormat, pluginFormat, func(_ string, rawJSON []byte, _ bool) []byte { return rawJSON }, sdktranslator.ResponseTransform{
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return []byte(`{"input_tokens":9,"translated":"by-response-translator"}`)
		},
	})
	t.Cleanup(func() {
		sdktranslator.Register(clientFormat, pluginFormat, nil, sdktranslator.ResponseTransform{})
	})
	adapter := newCurrentExecutorAdapterForTest(New(), "count-custom-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"total_tokens":9}`)}, nil
		},
	}, []sdktranslator.Format{pluginFormat}, []sdktranslator.Format{pluginFormat})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "custom-model",
		Format:  clientFormat,
		Payload: []byte(`{"model":"custom-model"}`),
	}, coreexecutor.Options{
		SourceFormat:   clientFormat,
		ResponseFormat: clientFormat,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), `{"input_tokens":9,"translated":"by-response-translator"}`; got != want {
		t.Fatalf("CountTokens() payload = %s, want response-translator output %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsIdenticalTokenCountTranslation(t *testing.T) {
	pluginFormat := sdktranslator.Format("count-identical-plugin")
	clientFormat := sdktranslator.Format("count-identical-client")
	raw := []byte(`{"total_tokens":9}`)
	sdktranslator.Register(clientFormat, pluginFormat, nil, sdktranslator.ResponseTransform{
		TokenCount: func(context.Context, int64) []byte { return raw },
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return []byte(`{"translated":"wrong fallback"}`)
		},
	})
	t.Cleanup(func() {
		sdktranslator.Register(clientFormat, pluginFormat, nil, sdktranslator.ResponseTransform{})
	})
	adapter := newCurrentExecutorAdapterForTest(New(), "count-identical-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: raw}, nil
		},
	}, []sdktranslator.Format{clientFormat}, []sdktranslator.Format{pluginFormat})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "custom-model",
		Format:  clientFormat,
		Payload: []byte(`{"model":"custom-model"}`),
	}, coreexecutor.Options{
		SourceFormat:   clientFormat,
		ResponseFormat: clientFormat,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if got, want := string(response.Payload), string(raw); got != want {
		t.Fatalf("CountTokens() payload = %s, want token-count translator output %s", got, want)
	}
}

func TestExecutorAdapterCountTokensKeepsNilTokenCountTranslation(t *testing.T) {
	pluginFormat := sdktranslator.Format("count-nil-plugin")
	clientFormat := sdktranslator.Format("count-nil-client")
	sdktranslator.Register(clientFormat, pluginFormat, nil, sdktranslator.ResponseTransform{
		TokenCount: func(context.Context, int64) []byte { return nil },
		NonStream: func(context.Context, string, []byte, []byte, []byte, *any) []byte {
			return []byte(`{"translated":"wrong fallback"}`)
		},
	})
	t.Cleanup(func() {
		sdktranslator.Register(clientFormat, pluginFormat, nil, sdktranslator.ResponseTransform{})
	})
	adapter := newCurrentExecutorAdapterForTest(New(), "count-nil-plugin", &fakeExecutor{
		identifier: "plugin-provider",
		countTokens: func(context.Context, pluginapi.ExecutorRequest) (pluginapi.ExecutorResponse, error) {
			return pluginapi.ExecutorResponse{Payload: []byte(`{"total_tokens":9}`)}, nil
		},
	}, []sdktranslator.Format{clientFormat}, []sdktranslator.Format{pluginFormat})

	response, errCount := adapter.CountTokens(context.Background(), nil, coreexecutor.Request{
		Model:   "custom-model",
		Format:  clientFormat,
		Payload: []byte(`{"model":"custom-model"}`),
	}, coreexecutor.Options{
		SourceFormat:   clientFormat,
		ResponseFormat: clientFormat,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if response.Payload != nil {
		t.Fatalf("CountTokens() payload = %s, want nil token-count translator output", response.Payload)
	}
}

func TestPluginExecutorTokenCountRejectsInvalidOrModelResponseCounts(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"fractional", `{"usage":{"prompt_tokens":1.5}}`},
		{"overflow", `{"usage":{"prompt_tokens":9223372036854775808}}`},
		{"overflow-wraps-positive", `{"usage":{"prompt_tokens":18446744073709551617}}`},
		{"string-number", `{"usage":{"prompt_tokens":"12"}}`},
		{"negative", `{"usage":{"prompt_tokens":-1}}`},
		{"model-response", `{"choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":12,"total_tokens":13}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if count, ok := pluginExecutorTokenCount([]byte(tc.payload)); ok {
				t.Fatalf("pluginExecutorTokenCount() = (%d, true), want no recognized count", count)
			}
		})
	}
}

func TestPluginExecutorTokenCountPrefersInputCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload string
		want    int64
	}{
		{"prompt-over-total", `{"usage":{"prompt_tokens":7,"total_tokens":10}}`, 7},
		{"input-over-total", `{"input_tokens":8,"total_tokens":10}`, 8},
		{"codex-nested-input-over-total", `{"response":{"usage":{"input_tokens":9,"total_tokens":10}}}`, 9},
		{"ignores-deeper-similar-keys", `{"wrapper":{"usage":{"prompt_tokens":99}},"total_tokens":5}`, 5},
		{"zero-is-valid", `{"usage":{"prompt_tokens":0}}`, 0},
		{"maximum-int64", `{"usage":{"prompt_tokens":9223372036854775807}}`, 9223372036854775807},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pluginExecutorTokenCount([]byte(tc.payload))
			if !ok || got != tc.want {
				t.Fatalf("pluginExecutorTokenCount() = (%d, %t), want (%d, true)", got, ok, tc.want)
			}
		})
	}
}
