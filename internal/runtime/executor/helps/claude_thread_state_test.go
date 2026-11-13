package helps

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
)

func TestClaudeThreadBetaStoreRestoresWireProfile(t *testing.T) {
	for _, betas := range []string{"", "message-threads-2026-08-12,effort-2025-11-24"} {
		t.Run(betas, func(t *testing.T) {
			store := &ClaudeThreadBetaStore{}
			first, ok, _ := store.Bind(context.Background(), "caller-a", "", http.Header{"Anthropic-Beta": []string{betas}}, ClaudeThreadBetaPolicy{})
			if !ok {
				t.Fatal("create was rejected")
			}
			first.Commit("message-a")
			for _, scope := range []string{"caller-b", "other-credential"} {
				if _, found, _ := store.Bind(context.Background(), scope, "message-a", http.Header{}, ClaudeThreadBetaPolicy{}); found {
					t.Fatalf("profile leaked into scope %q", scope)
				}
			}
			header := http.Header{"Anthropic-Beta": []string{"derived-beta"}, "anthropic-beta": []string{"custom-beta"}}
			second, found, _ := store.Bind(context.Background(), "caller-a", "message-a", header, ClaudeThreadBetaPolicy{})
			if !found || header.Get("Anthropic-Beta") != betas {
				t.Fatalf("restored header = %v, found=%v, want %q", header, found, betas)
			}
			if _, duplicate := header["anthropic-beta"]; duplicate {
				t.Fatal("restoring retained a duplicate wire-cased header")
			}
			second.Commit("message-b")
			thirdHeader := http.Header{"Anthropic-Beta": []string{"new-body-beta"}}
			if _, foundThird, _ := store.Bind(context.Background(), "caller-a", "message-b", thirdHeader, ClaudeThreadBetaPolicy{}); !foundThird || thirdHeader.Get("Anthropic-Beta") != betas {
				t.Fatalf("third-turn profile = %v, found=%v", thirdHeader, foundThird)
			}
		})
	}
}

func TestClaudeThreadBetaStoreEvictsOldestMessage(t *testing.T) {
	store := &ClaudeThreadBetaStore{}
	for i := range claudeThreadBetaLimit + 1 {
		request, _, _ := store.Bind(context.Background(), "scope", "", http.Header{"Anthropic-Beta": []string{"stable-beta"}}, ClaudeThreadBetaPolicy{})
		request.Commit(fmt.Sprintf("message-%d", i))
	}
	if _, found, _ := store.Bind(context.Background(), "scope", "message-0", http.Header{}, ClaudeThreadBetaPolicy{}); found {
		t.Fatal("oldest profile was not evicted")
	}
	if _, found, _ := store.Bind(context.Background(), "scope", fmt.Sprintf("message-%d", claudeThreadBetaLimit), http.Header{}, ClaudeThreadBetaPolicy{}); !found {
		t.Fatal("newest profile was evicted")
	}
}

func TestClaudeThreadBetaStoreConcurrentCallers(t *testing.T) {
	store := &ClaudeThreadBetaStore{}
	var wg sync.WaitGroup
	for caller := range 16 {
		wg.Go(func() {
			scope := fmt.Sprintf("caller-%d", caller)
			request, _, _ := store.Bind(context.Background(), scope, "", http.Header{"Anthropic-Beta": []string{scope}}, ClaudeThreadBetaPolicy{})
			request.Commit("shared-message-id")
			for range 10 {
				header := http.Header{}
				if _, found, _ := store.Bind(context.Background(), scope, "shared-message-id", header, ClaudeThreadBetaPolicy{}); !found || header.Get("Anthropic-Beta") != scope {
					t.Errorf("profile crossed callers: %v, scope=%s", header, scope)
				}
			}
		})
	}
	wg.Wait()
}

func TestClaudeThreadOversizedCompletionDoesNotPublishState(t *testing.T) {
	const start = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"message-a\"}}\n\n"
	const terminal = "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	const stop = "data: {\"type\":\"message_stop\"}\n\n"
	const pingPrefix = "data: {\"type\":\"ping\",\"padding\":\""
	const pingSuffix = "\"}\n\n"
	// Each event fits, but message_stop pushes the pending completion over its cap.
	pingSize := (claudeThreadEventLimit - 1 - len(terminal)) / 2
	ping := pingPrefix + strings.Repeat("x", pingSize-len(pingPrefix)-len(pingSuffix)) + pingSuffix
	body := io.MultiReader(strings.NewReader(start+terminal), strings.NewReader(ping), strings.NewReader(ping), strings.NewReader(stop))
	store := &ClaudeThreadBetaStore{}
	request, _, _ := store.Bind(context.Background(), "scope", "", http.Header{"Anthropic-Beta": []string{"profile"}}, ClaudeThreadBetaPolicy{})
	if _, errRead := io.Copy(io.Discard, request.StreamReader(body, nil)); errRead == nil {
		t.Fatal("oversized completion was accepted")
	}
	if _, found, _ := store.Bind(context.Background(), "scope", "message-a", http.Header{}, ClaudeThreadBetaPolicy{}); found {
		t.Fatal("rejected oversized completion published Thread state")
	}
}

func TestClaudeThreadStreamNeverCommitsFailedTurn(t *testing.T) {
	const start = "data: {\"type\":\"message_start\",\"message\":{\"id\":\"message-a\"}}\n\n"
	const terminal = "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
	const stop = "data: {\"type\":\"message_stop\"}\n\n"
	for _, test := range []struct {
		name string
		body io.Reader
	}{
		{name: "truncated", body: strings.NewReader(start + terminal)},
		{name: "malformed stop", body: strings.NewReader(start + terminal + "data: {\"type\":\"message_stop\",broken}\n\n")},
		{name: "error after terminal", body: strings.NewReader(start + terminal + "data: {\"type\":\"error\",\"error\":{\"message\":\"failed\"}}\n\n")},
		{name: "policy refusal", body: strings.NewReader(start + "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"refusal\",\"stop_details\":{\"type\":\"refusal\",\"category\":\"cyber\"}}}\n\n" + stop)},
		{name: "canceled", body: io.MultiReader(strings.NewReader(start), iotest.ErrReader(context.Canceled))},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &ClaudeThreadBetaStore{}
			request, _, _ := store.Bind(context.Background(), "scope", "", http.Header{"Anthropic-Beta": []string{"profile"}}, ClaudeThreadBetaPolicy{})
			data, errRead := io.ReadAll(request.StreamReader(test.body, nil))
			if test.name == "canceled" && !errors.Is(errRead, context.Canceled) {
				t.Fatalf("cancellation error = %v", errRead)
			}
			if test.name == "truncated" && (errRead == nil || strings.Contains(string(data), "message_delta")) {
				t.Fatalf("truncated turn released completion: data=%s, error=%v", data, errRead)
			}
			if _, found, _ := store.Bind(context.Background(), "scope", "message-a", http.Header{}, ClaudeThreadBetaPolicy{}); found {
				t.Fatal("failed turn published continuation state")
			}
		})
	}
}

func TestClaudeThreadPolicyChangesAndRequiredDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name     string
		policy   ClaudeThreadBetaPolicy
		betas    string
		conflict bool
	}{
		{"unchanged", ClaudeThreadBetaPolicy{Fingerprint: "old"}, "derived", false},
		{"changed header policy", ClaudeThreadBetaPolicy{Fingerprint: "new"}, "derived", true},
		{"policy with identical result", ClaudeThreadBetaPolicy{Fingerprint: "new"}, "thread,tool", false},
		{"payload removes capability", ClaudeThreadBetaPolicy{Fingerprint: "old", PayloadConfigured: true}, "thread", true},
		{"payload adds capability", ClaudeThreadBetaPolicy{Fingerprint: "old", PayloadConfigured: true, BodyBetas: "new"}, "thread,tool,new", true},
		{"header independently retains capability", ClaudeThreadBetaPolicy{Fingerprint: "old", PayloadConfigured: true}, "thread,tool", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &ClaudeThreadBetaStore{}
			first, _, err := s.Bind(t.Context(), "scope", "", http.Header{"Anthropic-Beta": {"thread,tool"}}, ClaudeThreadBetaPolicy{Fingerprint: "old", BodyBetas: "tool"})
			if err != nil {
				t.Fatal(err)
			}
			first.Commit("message")
			_, _, err = s.Bind(t.Context(), "scope", "message", http.Header{"Anthropic-Beta": {tc.betas}}, tc.policy)
			if (err != nil) != tc.conflict {
				t.Fatalf("conflict=%v error=%v", tc.conflict, err)
			}
		})
	}
}

func TestClaudeThreadAliasMapsAreBoundedAndCopied(t *testing.T) {
	store := &ClaudeThreadAliasStore{}
	aliases := map[string]string{"alias": "name"}
	store.Save("caller-a", "message", aliases)
	aliases["alias"] = "changed"
	found, ok := store.Load("caller-a", "message")
	if !ok || found["alias"] != "name" {
		t.Fatal("stored map is mutable")
	}
	found["alias"] = "changed"
	found, _ = store.Load("caller-a", "message")
	if found["alias"] != "name" {
		t.Fatal("load exposed mutable map")
	}
	if _, ok := store.Load("caller-b", "message"); ok {
		t.Fatal("alias crossed caller boundary")
	}
	var wg sync.WaitGroup
	for i := range 1100 {
		wg.Go(func() { store.Save("caller-a", fmt.Sprint(i), nil); store.Load("caller-a", fmt.Sprint(i)) })
	}
	wg.Wait()
	if store.cache().Len() != 1024 {
		t.Fatalf("entries=%d", store.cache().Len())
	}
	if _, ok := store.Load("caller-a", "message"); ok {
		t.Fatal("oldest alias state was retained")
	}
}

func TestClaudeThreadCompletionPublishesBeforeTerminal(t *testing.T) {
	store := &ClaudeThreadBetaStore{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	request, _, _ := store.Bind(ctx, "scope", "", http.Header{"Anthropic-Beta": {"tool"}}, ClaudeThreadBetaPolicy{})
	committed := false
	request.OnCommit(func(string) { committed = true })
	body := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"message\"}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n"
	reader := request.StreamReader(strings.NewReader(body), nil)
	one := make([]byte, 1)
	var output strings.Builder
	for {
		n, err := reader.Read(one)
		output.Write(one[:n])
		if strings.Contains(output.String(), "message_delta") && !committed {
			t.Fatal("completion exposed before routing and alias publication")
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if !committed {
		t.Fatal("successful stream did not publish state")
	}
	cancel()
	request.Commit("canceled")
	if _, found, _ := store.Bind(t.Context(), "scope", "canceled", http.Header{}, ClaudeThreadBetaPolicy{}); found {
		t.Fatal("canceled request published state")
	}
}

func TestClaudeThreadJSONResponseRequiresSuccessfulMessage(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"id":"message","type":"message","stop_reason":"end_turn"}`, true},
		{`{"id":"message","type":"message","stop_reason":"refusal"}`, true},
		{`{"id":"message","type":"message","stop_reason":"refusal","stop_details":{"type":"refusal"}}`, false},
		{`{"id":"message","type":"message"}`, false},
		{`{"id":"message","type":"error","stop_reason":"end_turn"}`, false},
		{`{"id":"message","type":"message","stop_reason":"end_turn",broken}`, false},
	} {
		if got := ClaudeThreadResponseID([]byte(tc.body)); (got != "") != tc.valid {
			t.Errorf("response=%s id=%q", tc.body, got)
		}
	}
}
