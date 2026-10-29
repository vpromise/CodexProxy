package pluginhost

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRequestHeaderRemovalSurvivesCallerMerge(t *testing.T) {
	for _, afterAuth := range []bool{false, true} {
		stage := "before"
		if afterAuth {
			stage = "after"
		}
		for _, restore := range []bool{false, true} {
			name := stage + "/remove"
			if restore {
				name = stage + "/restore"
			}
			t.Run(name, func(t *testing.T) {
				host := newHostWithRecords(
					capabilityRecord{id: "remove", priority: 20, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
							return pluginapi.RequestInterceptResponse{ClearHeaders: []string{"x-remove"}}, nil
						}),
					}}},
					capabilityRecord{id: "next", priority: 10, plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
						RequestInterceptor: requestInterceptorFunc(func(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
							if req.Headers.Get("X-Remove") != "" {
								t.Fatal("later plugin received the removed header")
							}
							if restore {
								return pluginapi.RequestInterceptResponse{Headers: http.Header{"X-Remove": {"new"}}}, nil
							}
							return pluginapi.RequestInterceptResponse{}, nil
						}),
					}}},
				)
				headers := http.Header{"X-Remove": {"old"}, "X-Keep": {"retained"}}
				req := pluginapi.RequestInterceptRequest{Headers: headers, Body: []byte(`{"input":[]}`)}
				invoke := host.InterceptRequestBeforeAuth
				if afterAuth {
					invoke = host.InterceptRequestAfterAuth
				}
				got := invoke(context.Background(), req)
				// Both handler and conductor callers merge the response into original headers.
				merged := mergeHeaders(headers, got.Headers, got.ClearHeaders)
				want := ""
				if restore {
					want = "new"
					if len(got.ClearHeaders) != 0 {
						t.Fatalf("restored header still marked for deletion: %v", got.ClearHeaders)
					}
				}
				if merged.Get("X-Remove") != want || merged.Get("X-Keep") != "retained" {
					t.Fatalf("caller headers = %v, want removed/restored=%q with retained sibling", merged, want)
				}
				if headers.Get("X-Remove") != "old" {
					t.Fatal("original headers mutated")
				}
			})
		}
	}
}
