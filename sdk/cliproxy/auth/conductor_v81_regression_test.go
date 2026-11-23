package auth

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAfterAuthInterceptorReplacesHeadersAcrossCaseVariants(t *testing.T) {
	current := http.Header{
		"X-Route":  {"stale-canonical"},
		"x-route":  {"stale-lowercase"},
		"X-REMOVE": {"remove"},
		"x-empty":  {"also remove"},
		"X-Keep":   {"keep"},
	}
	before := current.Clone()
	opts := cliproxyexecutor.Options{
		Headers: current,
		RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{
				ClearHeaders: []string{"x-remove"},
				Headers:      http.Header{"X-Route": {"new", "second"}, "X-Empty": nil},
			}
		},
	}
	_, got, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "codex", cliproxyexecutor.Request{Model: "fixture"}, opts, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	want := http.Header{"X-Route": {"new", "second"}, "X-Keep": {"keep"}}
	if !reflect.DeepEqual(got.Headers, want) {
		t.Fatalf("headers = %v, want %v", got.Headers, want)
	}
	got.Headers["X-Keep"][0] = "changed copy"
	if !reflect.DeepEqual(current, before) {
		t.Fatalf("original headers changed: %v, want %v", current, before)
	}
}

func TestWrapStreamResultEmitsForceMappedTailOnce(t *testing.T) {
	const partial = `data: {"partial":true`
	const completed = `"type":"response.completed"`
	input := `data: {"type":"response.created","response":{"model":"upstream-model"}}` + "\n\n" +
		`data: {"type":"response.completed","response":{"model":"upstream-model"}}` + "\n" + partial
	first := cliproxyexecutor.StreamChunk{Payload: []byte(input)}
	remaining := make(chan cliproxyexecutor.StreamChunk)
	close(remaining)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	manager := NewManager(nil, nil, nil)
	credential := &Auth{ID: "fixture", Provider: "codex", Status: StatusActive}
	alias := OAuthModelAliasResult{ForceMapping: true, OriginalAlias: "local-alias"}
	outcome := Result{AuthID: credential.ID, Provider: "codex", Model: "local-alias", RouteModel: "local-alias", AttemptStartedAt: time.Now()}
	result := manager.wrapStreamResult(ctx, credential, outcome, nil, first, remaining, alias, false, func() {})

	var output strings.Builder
	for {
		select {
		case <-ctx.Done():
			t.Fatal("stream did not close")
		case chunk, open := <-result.Chunks:
			if !open {
				got := output.String()
				if strings.Count(got, completed) != 1 || strings.Count(got, partial) != 1 ||
					strings.Count(got, `"model":"local-alias"`) != 2 || strings.Contains(got, "upstream-model") {
					t.Fatalf("stream lost, repeated or failed to rewrite payload: %q", got)
				}
				return
			}
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			output.Write(chunk.Payload)
		}
	}
}
