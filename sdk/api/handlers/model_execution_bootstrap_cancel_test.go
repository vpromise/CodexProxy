package handlers

import (
	"context"
	"errors"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestExecuteModelStreamCanceledBeforeFirstChunkCompletesAsCanceled(t *testing.T) {
	var cancelCurrent context.CancelFunc
	executor := &modelExecutionCaptureExecutor{
		stream: func(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			cancelCurrent()
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		},
	}
	h := newModelExecutionHandler(t, "nested-cancel-before-read-model", executor, &sdkconfig.SDKConfig{})
	completions := make(chan pluginapi.RequestCompletion, 1)
	h.SetPluginHost(&handlerInterceptorTestHost{
		completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) {
			completions <- completion
		},
	})
	req := ModelExecutionRequest{
		Model:         "nested-cancel-before-read-model",
		EntryProtocol: "openai",
		Stream:        true,
		Body:          []byte(`{"model":"nested-cancel-before-read-model"}`),
	}
	for round := range 2000 {
		ctx, cancel := context.WithCancel(context.Background())
		cancelCurrent = cancel
		result, errMsg := h.ExecuteModelStream(ctx, req)
		// The local auth bootstrap can return cancellation as a startup 499.
		// Both that path and a returned stream must report canceled completion.
		if errMsg != nil {
			if errMsg.StatusCode != 499 || !errors.Is(errMsg.Error, context.Canceled) {
				t.Fatalf("round %d: ExecuteModelStream() error = %+v", round, errMsg)
			}
		} else {
			for range result.Chunks {
			}
		}
		completion := <-completions
		if completion.Outcome != pluginapi.RequestCompletionCanceled {
			t.Fatalf("round %d: completion = %+v, want %s", round, completion, pluginapi.RequestCompletionCanceled)
		}
	}
}

func TestExecuteModelStreamCanceledAfterFilteredBootstrapCompletesAsCanceled(t *testing.T) {
	var cancelCurrent context.CancelFunc
	executor := &modelExecutionCaptureExecutor{
		stream: func(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
			chunks := make(chan coreexecutor.StreamChunk, 1)
			chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"ok\":true}\n\n")}
			close(chunks)
			return &coreexecutor.StreamResult{Chunks: chunks}, nil
		},
	}
	h := newModelExecutionHandler(t, "nested-cancel-filtered-bootstrap-model", executor, &sdkconfig.SDKConfig{})
	completions := make(chan pluginapi.RequestCompletion, 1)
	h.SetPluginHost(&handlerInterceptorTestHost{
		interceptStreamChunk: func(_ context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
			if req.ChunkIndex != pluginapi.StreamChunkHeaderInitIndex {
				// Cancel after auth bootstrap, then filter the only payload so the
				// handler must distinguish canceled closure from successful EOF.
				cancelCurrent()
				return pluginapi.StreamChunkInterceptResponse{DropChunk: true}
			}
			return pluginapi.StreamChunkInterceptResponse{}
		},
		completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) {
			completions <- completion
		},
	})
	req := ModelExecutionRequest{
		Model:         "nested-cancel-filtered-bootstrap-model",
		EntryProtocol: "openai",
		Stream:        true,
		Body:          []byte(`{"model":"nested-cancel-filtered-bootstrap-model"}`),
	}
	for round := range 2000 {
		ctx, cancel := context.WithCancel(context.Background())
		cancelCurrent = cancel
		result, errMsg := h.ExecuteModelStream(ctx, req)
		// The local auth bootstrap can return cancellation as a startup 499.
		// Both that path and a returned stream must report canceled completion.
		if errMsg != nil {
			if errMsg.StatusCode != 499 || !errors.Is(errMsg.Error, context.Canceled) {
				t.Fatalf("round %d: ExecuteModelStream() error = %+v", round, errMsg)
			}
		} else {
			for range result.Chunks {
			}
		}
		completion := <-completions
		if completion.Outcome != pluginapi.RequestCompletionCanceled {
			t.Fatalf("round %d: completion = %+v, want %s", round, completion, pluginapi.RequestCompletionCanceled)
		}
	}
}
