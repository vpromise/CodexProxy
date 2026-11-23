package claudereplay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tidwall/gjson"
)

const (
	originalUser = `{"role":"user","content":[{"type":"text","text":"Original input"}]}`
	firstBlock   = `{"type":"redacted_thinking","data":"synthetic-opaque-first"}`
	secondBlock  = `{"type":"redacted_thinking","data":"synthetic-opaque-second"}`
	toolBlock    = `{"type":"tool_use","id":"toolu_replay","name":"lookup","input":{"query":"needle"}}`
	doneTool     = `{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_replay","content":"result"}]}`
)

func wrappedRequest(messages ...string) []byte {
	return []byte(`{"model":"claude-test","system":[{"type":"text","text":"Original instructions"}],"messages":[` + strings.Join(messages, ",") + `]}`)
}

func assistant(parts ...string) string {
	return `{"role":"assistant","content":[` + strings.Join(parts, ",") + `]}`
}

func assertReplayUnavailable(t *testing.T, err error) {
	t.Helper()
	var replayError UnavailableError
	if !errors.As(err, &replayError) || !replayError.IsRequestScoped() || replayError.StatusCode() != 400 {
		t.Fatalf("expected a request-scoped replay rejection, got %v", err)
	}
}

func TestReplayReferencesAndEncryptedRowsAreScopeBound(t *testing.T) {
	store, err := newStore(bytes.Repeat([]byte{3}, 32), []string{"first", "second"})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Caller: "caller", Pool: "first", Account: "account", Model: "test-model", Endpoint: "test-endpoint"}
	id := newID()
	reference := store.ref(scope, id, 7)
	got, index, ok := store.parseRef(scope, reference)
	if !ok || got != id || index != 7 || len(reference) > 64 {
		t.Fatal("unexpected reference round trip")
	}
	for _, wrong := range []Scope{{Caller: "other", Pool: scope.Pool}, {Caller: scope.Caller, Pool: "second"}} {
		if _, _, ok := store.parseRef(wrong, reference); ok {
			t.Fatal("reference survived an authorization change")
		}
	}
	modified := []byte(reference)
	modified[len(refPrefix)] ^= 4
	if _, _, ok := store.parseRef(scope, string(modified)); ok {
		t.Fatal("accepted a changed reference")
	}

	expiry := time.Now().Truncate(time.Microsecond).Add(time.Minute)
	sealed := store.seal("key", store.owner(scope), expiry, []byte(firstBlock))
	if bytes.Contains(sealed, []byte("synthetic-opaque-first")) {
		t.Fatal("stored bytes contain plaintext")
	}
	if cleartext, err := store.unseal("key", store.owner(scope), expiry, sealed); err != nil || string(cleartext) != firstBlock {
		t.Fatalf("encrypted row failed to round trip: %v", err)
	}
	if _, err := store.unseal("key", store.owner(scope), expiry.Add(time.Second), sealed); err == nil {
		t.Fatal("row expiry could be extended without authenticating the record")
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := store.unseal("key", store.owner(scope), expiry, sealed); err == nil {
		t.Fatal("changed ciphertext authenticated")
	}
}

func TestReplayRestoresOnlyVerifiedSequenceAndKeepsCacheHints(t *testing.T) {
	store, err := newStore(bytes.Repeat([]byte{4}, 32), []string{"pool"})
	if err != nil {
		t.Fatal(err)
	}
	scope := Scope{Caller: "caller", Pool: "pool", Account: "account", Model: "model", Endpoint: "endpoint"}
	id := newID()
	body := wrappedRequest(originalUser, assistant(secondBlock, toolBlock), doneTool)
	original := []byte(`[` + strings.Join([]string{firstBlock, secondBlock, toolBlock}, ",") + `]`)
	value := record{
		ID: id, Prefix: advancePrefix(initialPrefix(body), gjson.Parse(originalUser)), MessageCount: 1, Content: original, Binding: store.binding(scope),
	}
	references := references{
		claims: map[string][]claim{id: {{index: 1, carrier: redactedPrefix + "synthetic-opaque-second", active: true}}},
		calls:  []string{"toolu_replay"},
	}
	for _, tc := range []struct {
		name, body string
	}{
		{"changed visible prefix", strings.ReplaceAll(string(body), "Original input", "Edited input")},
		{"changed system message", strings.ReplaceAll(string(body), "Original instructions", "Edited instructions")},
		{"changed tool input", strings.ReplaceAll(string(body), "needle", "something else")},
		{"signed block beyond tool boundary", string(wrappedRequest(originalUser, assistant(toolBlock, secondBlock), doneTool))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := store.restore([]byte(tc.body), gjson.Get(tc.body, "messages").Array(), scope, references, []record{value})
			assertReplayUnavailable(t, err)
		})
	}
	badClaims := references.claims[id]
	references.claims[id] = append([]claim{{index: 1, carrier: "modified", active: true}}, badClaims...)
	_, _, err = store.restore(body, gjson.GetBytes(body, "messages").Array(), scope, references, []record{value})
	assertReplayUnavailable(t, err)
	references.claims[id] = append(badClaims, claim{index: 0, carrier: redactedPrefix + "synthetic-opaque-first", active: true})
	_, _, err = store.restore(body, gjson.GetBytes(body, "messages").Array(), scope, references, []record{value})
	assertReplayUnavailable(t, err)
	references.claims[id] = badClaims
	body = bytes.Replace(body, []byte(`"input":{"query":"needle"}`), []byte(`"input":{"query":"needle"},"cache_control":{"type":"ephemeral"}`), 1)
	got, _, err := store.restore(body, gjson.GetBytes(body, "messages").Array(), scope, references, []record{value})
	if err != nil || gjson.GetBytes(got, "messages.1.content.0.data").String() != "synthetic-opaque-first" ||
		gjson.GetBytes(got, "messages.1.content.1.data").String() != "synthetic-opaque-second" ||
		gjson.GetBytes(got, "messages.1.content.2.cache_control.type").String() != "ephemeral" ||
		gjson.GetBytes(got, "messages.2.content.0.content").String() != "result" {
		t.Fatalf("expected exact replay before the original tool result, got error %v", err)
	}
}

func TestPostgresReplayAcrossInstancesAndExpiry(t *testing.T) {
	dsn := os.Getenv("CODEXPROXY_TEST_CLAUDE_REPLAY_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires an isolated Claude replay PostgreSQL instance")
	}
	ctx := context.Background()
	key := bytes.Repeat([]byte{5}, 32)
	pool := fmt.Sprintf("fixture-%x", newID())
	scope := Scope{Caller: "caller", Pool: pool, Account: "account", Model: "model", Endpoint: "endpoint"}
	first, err := open(ctx, dsn, key, []string{pool})
	if err != nil {
		t.Fatal(err)
	}
	_, turn, err := first.Prepare(ctx, scope, []byte(`{"input":"hello"}`), wrappedRequest(originalUser))
	if err != nil || turn == nil {
		t.Fatalf("prepare: %v", err)
	}
	raw := []byte(`[` + strings.Join([]string{firstBlock, secondBlock, toolBlock}, ",") + `]`)
	handle := ReasoningItemID(turn.ResponseContext(ctx), "rs_legacy", 1)
	turn.Complete(ctx, raw)
	first.Close()

	next, err := open(ctx, dsn, key, []string{pool})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	owner := next.owner(scope)
	var sealed []byte
	if err := next.db.QueryRowContext(ctx, "SELECT body FROM codexproxy_claude_replay_v1 WHERE owner_key = $1", owner).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("synthetic-opaque-first")) || bytes.Contains(sealed, []byte("needle")) {
		t.Fatal("database row contains plaintext replay content")
	}
	inputObject := func(ref string) []byte {
		items := []map[string]string{}
		if ref != "" {
			items = append(items, map[string]string{"type": "reasoning", "id": ref})
		}
		items = append(items, map[string]string{"type": "function_call", "call_id": "toolu_replay"}, map[string]string{"type": "function_call_output", "call_id": "toolu_replay"})
		input, _ := json.Marshal(map[string]any{"input": items})
		return input
	}
	for _, receipt := range []string{handle, ""} {
		body, plan, err := next.Prepare(ctx, scope, inputObject(receipt), wrappedRequest(originalUser, assistant(toolBlock), doneTool))
		if err != nil || gjson.GetBytes(body, "messages.1.content.#").Int() != 3 || plan == nil || plan.RequestLogBody(body) != nil {
			t.Fatalf("second instance did not restore the full response: %v", err)
		}
	}
	wrong := scope
	wrong.Account = "another-upstream-account"
	_, _, err = next.Prepare(ctx, wrong, inputObject(handle), wrappedRequest(originalUser, assistant(toolBlock), doneTool))
	assertReplayUnavailable(t, err)
	wrong = scope
	wrong.Caller = "another-downstream-caller"
	_, _, err = next.Prepare(ctx, wrong, inputObject(handle), wrappedRequest(originalUser, assistant(toolBlock), doneTool))
	assertReplayUnavailable(t, err)
	payload, _, err := next.Prepare(ctx, wrong, inputObject(""), wrappedRequest(originalUser, assistant(toolBlock), doneTool))
	if err != nil || gjson.GetBytes(payload, "messages.1.content.#").Int() != 1 {
		t.Fatalf("a bare tool ID found another caller's response: %v", err)
	}
	if _, err := next.db.ExecContext(ctx, "UPDATE codexproxy_claude_replay_v1 SET expires_at = CURRENT_TIMESTAMP - interval '1 second' WHERE owner_key = $1", owner); err != nil {
		t.Fatal(err)
	}
	_, _, err = next.Prepare(ctx, scope, inputObject(handle), wrappedRequest(originalUser, assistant(toolBlock), doneTool))
	assertReplayUnavailable(t, err)
	if err := next.db.Close(); err != nil {
		t.Fatal(err)
	}
	_, _, err = next.Prepare(ctx, scope, inputObject(handle), wrappedRequest(originalUser, assistant(toolBlock), doneTool))
	assertReplayUnavailable(t, err)
	plain := wrappedRequest(originalUser)
	payload, _, err = next.Prepare(ctx, scope, []byte(`{"input":"new request"}`), plain)
	if err != nil || !bytes.Equal(payload, plain) {
		t.Fatalf("a store outage blocked an ordinary request: %v", err)
	}
}
