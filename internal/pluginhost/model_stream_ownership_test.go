package pluginhost

import (
	"context"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"net/http"
	"testing"
)

func modelStreamRequestForOwnershipTest(t *testing.T, callback string) []byte {
	t.Helper()
	data, err := json.Marshal(rpcHostModelExecutionRequest{HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{EntryProtocol: "openai", ExitProtocol: "openai", Model: "test", Stream: true}, HostCallbackID: callback})
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func TestModelStreamRejectsForeignPlugin(t *testing.T) {
	for _, method := range []string{pluginabi.MethodHostModelStreamRead, pluginabi.MethodHostModelStreamClose} {
		t.Run(method, func(t *testing.T) {
			h := New()
			chunks := make(chan handlers.ModelExecutionChunk, 1)
			chunks <- handlers.ModelExecutionChunk{Payload: []byte("owner")}
			ctxSeen := context.Background()
			h.SetModelExecutor(&fakeHostModelExecutor{executeModelStream: func(ctx context.Context, _ handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
				ctxSeen = ctx
				return handlers.ModelExecutionStream{StatusCode: http.StatusOK, Chunks: chunks}, nil
			}})
			owner := withHostCallbackPluginID(context.Background(), "plugin-a")
			other := withHostCallbackPluginID(context.Background(), "plugin-b")
			data, err := h.callFromPlugin(owner, pluginabi.MethodHostModelExecuteStream, modelStreamRequestForOwnershipTest(t, ""))
			if err != nil {
				t.Fatal(err)
			}
			result, err := decodeRPCEnvelope[pluginapi.HostModelStreamResponse](data)
			if err != nil {
				t.Fatal(err)
			}
			req, _ := json.Marshal(pluginapi.HostModelStreamReadRequest{StreamID: result.StreamID})
			_, err = h.callFromPlugin(other, method, req)
			if method == pluginabi.MethodHostModelStreamRead && err == nil {
				t.Fatal("foreign plugin consumed stream")
			}
			if ctxSeen.Err() != nil {
				t.Fatal("foreign plugin canceled owner stream")
			}
			data, err = h.callFromPlugin(owner, pluginabi.MethodHostModelStreamRead, req)
			if err != nil {
				t.Fatal(err)
			}
			read, err := decodeRPCEnvelope[pluginapi.HostModelStreamReadResponse](data)
			if err != nil {
				t.Fatal(err)
			}
			if string(read.Payload) != "owner" || read.Done {
				t.Fatalf("owner read = %#v", read)
			}
			_, err = h.callFromPlugin(owner, pluginabi.MethodHostModelStreamClose, req)
			if err != nil {
				t.Fatal(err)
			}
			if ctxSeen.Err() == nil {
				t.Fatal("owner close failed to cancel")
			}
		})
	}
}
func TestModelStreamRejectsForeignCallbackBeforeExecution(t *testing.T) {
	h := New()
	calls := 0
	h.SetModelExecutor(&fakeHostModelExecutor{executeModelStream: func(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
		calls++
		return handlers.ModelExecutionStream{Chunks: make(chan handlers.ModelExecutionChunk)}, nil
	}})
	id, closeScope := h.openCallbackContextForPlugin(context.Background(), "plugin-a")
	defer closeScope()
	_, err := h.callFromPlugin(withHostCallbackPluginID(context.Background(), "plugin-b"), pluginabi.MethodHostModelExecuteStream, modelStreamRequestForOwnershipTest(t, id))
	if err == nil || calls != 0 {
		t.Fatalf("foreign callback: err=%v calls=%d", err, calls)
	}
}
func TestModelStreamRejectsClosedCallbackAtRegistration(t *testing.T) {
	h := New()
	id, closeScope := h.openCallbackContext(context.Background())
	defer closeScope()
	var seen context.Context
	h.SetModelExecutor(&fakeHostModelExecutor{executeModelStream: func(ctx context.Context, _ handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
		seen = ctx
		closeScope()
		return handlers.ModelExecutionStream{Chunks: make(chan handlers.ModelExecutionChunk)}, nil
	}})
	_, err := h.callFromPlugin(context.Background(), pluginabi.MethodHostModelExecuteStream, modelStreamRequestForOwnershipTest(t, id))
	if err == nil {
		t.Error("returned a stream ID after its callback closed")
	}
	if seen == nil || seen.Err() == nil {
		t.Error("stream not canceled after scope teardown")
	}
	h.modelStreams.mu.Lock()
	remaining := len(h.modelStreams.streams)
	h.modelStreams.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("remaining streams = %d", remaining)
	}
}
