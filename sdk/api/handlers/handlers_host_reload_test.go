package handlers

import (
	"context"
	"sync"
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHandlerHostReloadConcurrentWithRequests(t *testing.T) {
	h := &BaseAPIHandler{}
	host := &handlerDirectExecutorInterceptorHost{}
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			h.SetPluginHost(host)
			h.SetModelRouterHost(host)
			h.SetPluginHost(nil)
			h.SetModelRouterHost(nil)
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			req := coreexecutor.Request{Model: "synthetic", Payload: []byte(`{"input":"safe fixture"}`)}
			got, _, err := h.applyRequestInterceptorsBeforeAuth(context.Background(), "openai", req.Model, "", req, coreexecutor.Options{}, "")
			if err != nil || string(got.Payload) != string(req.Payload) {
				t.Error("reload changed request payload")
			}
			_ = h.modelRouterHost()
			_ = h.pluginExecutorHost()
		}
	}()
	close(start)
	wg.Wait()
}

func TestHandlerHostCallbackCanReload(t *testing.T) {
	h := &BaseAPIHandler{}
	h.SetPluginHost(&handlerInterceptorTestHost{interceptRequestBeforeAuth: func(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
		h.SetPluginHost(nil)
		h.SetModelRouterHost(nil)
		return pluginapi.RequestInterceptResponse{Body: req.Body}
	}})
	_, _, err := h.applyRequestInterceptorsBeforeAuth(context.Background(), "openai", "synthetic", "", coreexecutor.Request{Payload: []byte(`{}`)}, coreexecutor.Options{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if h.interceptorHost() != nil || h.modelRouterHost() != nil {
		t.Fatal("callback reload was not applied")
	}
}
