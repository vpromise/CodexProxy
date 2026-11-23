package executor

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudereplay"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/core"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func replayFixtureSignature(model string) string {
	var channel []byte
	channel = protowire.AppendVarint(protowire.AppendTag(channel, 1, protowire.VarintType), 12)
	channel = protowire.AppendVarint(protowire.AppendTag(channel, 2, protowire.VarintType), 2)
	channel = protowire.AppendString(protowire.AppendTag(channel, 6, protowire.BytesType), model)
	container := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), channel)
	payload := protowire.AppendBytes(protowire.AppendTag(nil, 2, protowire.BytesType), container)
	payload = protowire.AppendVarint(protowire.AppendTag(payload, 3, protowire.VarintType), 1)
	return base64.StdEncoding.EncodeToString(payload)
}

func TestClaudeResponsesReplayManagerCrossNode(t *testing.T) {
	dsn := os.Getenv("CODEXPROXY_TEST_CLAUDE_REPLAY_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires an isolated Claude replay PostgreSQL instance")
	}
	t.Run("api_key", func(t *testing.T) { testClaudeResponsesReplayManagerCrossNode(t, dsn, false) })
	t.Run("oauth", func(t *testing.T) { testClaudeResponsesReplayManagerCrossNode(t, dsn, true) })
}

func testClaudeResponsesReplayManagerCrossNode(t *testing.T, dsn string, oauth bool) {
	t.Helper()
	pool := "replay-" + uuid.NewString()
	t.Setenv("CODEXPROXY_CLAUDE_REPLAY_POOLS", pool)
	t.Setenv("CODEXPROXY_CLAUDE_REPLAY_POSTGRES_DSN", dsn)
	t.Setenv("CODEXPROXY_CLAUDE_REPLAY_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)))
	open := func() *claudereplay.Store {
		t.Helper()
		store, err := claudereplay.OpenFromEnv(t.Context())
		if err != nil || store == nil {
			t.Fatalf("open replay: %v", err)
		}
		t.Cleanup(store.Close)
		return store
	}
	nodeA, nodeB := open(), open()

	const model = "claude-opus-5-replay-test"
	const callerA, callerB = "replay-caller-a", "replay-caller-b"
	sigA, sigB := replayFixtureSignature("claude-opus-5-5-first"), replayFixtureSignature("claude-opus-5-5-second")
	sent := make(chan []byte, 32)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "unable to read fixture", 400)
			return
		}
		calls.Add(1)
		sent <- request
		w.Header().Set("Content-Type", "text/event-stream")
		var frames []string
		if !bytes.Contains(request, []byte(`"type":"tool_result"`)) {
			wireName := gjson.GetBytes(request, "tools.0.name").String()
			frames = []string{
				`{"type":"message_start","message":{"id":"msg_replay","model":"` + model + `","usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
				fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":%q}}`, sigA),
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"Progress."}}`,
				fmt.Sprintf(`{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":%q}}`, sigB),
				`{"type":"content_block_stop","index":1}`,
				fmt.Sprintf(`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_replay_123","name":%q,"input":{}}}`, wireName),
				`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":\"hello\"}"}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`,
				`{"type":"message_stop"}`,
			}
		} else {
			frames = []string{
				`{"type":"message_start","message":{"id":"msg_after","model":"` + model + `","usage":{"input_tokens":1,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Finished"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
				`{"type":"message_stop"}`,
			}
		}
		for _, frame := range frames {
			_, _ = fmt.Fprint(w, "data: "+frame+"\n\n")
		}
	}))
	defer upstream.Close()

	candidate := func() *coreauth.Auth {
		a := &coreauth.Auth{
			ID: uuid.NewString(), Provider: "claude", Status: coreauth.StatusActive,
			Attributes: map[string]string{"api_key": "replay-upstream-key", "base_url": upstream.URL},
		}
		if oauth {
			delete(a.Attributes, "api_key")
			a.Attributes["auth_kind"] = "oauth"
			a.Metadata = claudeOAuthCancellationTestMetadata()
			const token = "sk-ant-oat-fixture-replay"
			a.Metadata["access_token"] = token
			a.Metadata["organization_uuid"] = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
			a.Metadata[claudeAccountProfileTokenSHA256Key] = claudeAccountProfileTokenSHA256(token)
		}
		return a
	}
	build := func(store *claudereplay.Store, a *coreauth.Auth) (*coreauth.Manager, *config.Config) {
		t.Helper()
		cfg := &config.Config{}
		cfg.APIKeys = []string{callerA, callerB}
		cfg.AccessIsolation = config.AccessIsolation{
			Enabled: true,
			Pools:   []config.AccountPool{{ID: pool, Provider: "claude", Accounts: []string{coreauth.AccountIdentity(a)}}},
			Keys:    []config.ClientKeyBinding{{Key: callerA, Pool: pool}, {Key: callerB, Pool: pool}},
		}
		m := coreauth.NewManager(nil, nil, nil)
		m.SetConfig(cfg)
		m.RegisterExecutor(NewClaudeExecutorWithResponsesReplay(cfg, store))
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(a.ID) })
		if _, err := m.Register(t.Context(), a); err != nil {
			t.Fatal(err)
		}
		return m, cfg
	}
	accountA, accountB := candidate(), candidate()
	m1, _ := build(nodeA, accountA)
	m2, cfgB := build(nodeB, accountB)
	const toolDecl = `"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}]`
	request := func(input string) coreexec.Request {
		return coreexec.Request{
			Model: model, Payload: []byte(`{"model":"` + model + `","instructions":"Stable instructions",` + toolDecl + `,"input":` + input + `}`),
		}
	}
	const user = `{"role":"user","content":"hello"}`
	options := coreexec.Options{SourceFormat: translator.FormatOpenAIResponse}
	ctx := coreauth.WithClientIdentity(t.Context(), callerA, "config-inline")
	result := func(firstStream bool) gjson.Result {
		t.Helper()
		start := request("[" + user + "]")
		if !firstStream {
			r, err := m1.Execute(ctx, []string{"claude"}, start, options)
			if err != nil {
				t.Fatal(err)
			}
			return gjson.GetBytes(r.Payload, "output")
		}
		opts := options
		opts.Stream = true
		r, err := m1.ExecuteStream(ctx, []string{"claude"}, start, opts)
		if err != nil {
			t.Fatal(err)
		}
		var output gjson.Result
		var added []string
		for chunk := range r.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
			for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				root := gjson.ParseBytes(bytes.TrimSpace(line[len("data:"):]))
				if root.Get("type").String() == "response.output_item.added" && root.Get("item.type").String() == "reasoning" {
					added = append(added, root.Get("item.id").String())
				}
				if root.Get("type").String() == "response.completed" {
					output = root.Get("response.output")
				}
			}
		}
		if len(added) != 2 || added[0] != output.Get("0.id").String() || added[1] != output.Get("1.id").String() {
			t.Fatalf("reasoning item IDs changed during the stream: added=%v output=%s", added, output.Raw)
		}
		return output
	}
	for _, firstStream := range []bool{false, true} {
		t.Run(fmt.Sprintf("first_stream=%v", firstStream), func(t *testing.T) {
			output := result(firstStream)
			firstRequest := <-sent
			items := output.Array()
			if len(items) != 3 || !strings.HasPrefix(items[1].Get("id").String(), "rs_cpr1_") ||
				items[1].Get("encrypted_content").String() != sigB || items[2].Get("name").String() != "lookup" {
				t.Fatalf("first node did not advertise two original signed reasoning items: output=%s request=%s", output.Raw, firstRequest)
			}
			// Pass only the second signed item, so the missing first one has to
			// come from PostgreSQL. The tool call and result are transmitted once.
			input := "[" + strings.Join([]string{user, items[1].Raw, items[2].Raw, `{"type":"function_call_output","call_id":"toolu_replay_123","output":"fixture result"}`}, ",") + "]"
			if firstStream {
				if _, err := m2.Execute(ctx, []string{"claude"}, request(input), options); err != nil {
					t.Fatal(err)
				}
			} else {
				opts := options
				opts.Stream = true
				stream, err := m2.ExecuteStream(ctx, []string{"claude"}, request(input), opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range stream.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			}
			body := <-sent
			index := gjson.GetBytes(firstRequest, "messages.#").Int()
			assistant := gjson.GetBytes(body, fmt.Sprintf("messages.%d.content", index))
			if assistant.Get("#").Int() != 3 ||
				assistant.Get("0.signature").String() != sigA ||
				assistant.Get("1.signature").String() != sigB ||
				assistant.Get("2.name").String() != gjson.GetBytes(body, "tools.0.name").String() ||
				gjson.GetBytes(body, fmt.Sprintf("messages.%d.content.0.tool_use_id", index+1)).String() != "toolu_replay_123" {
				t.Fatal("second node did not send the exact signed blocks and the original tool pair")
			}
			before := calls.Load()
			wrongCaller := coreauth.WithClientIdentity(t.Context(), callerB, "config-inline")
			_, err := m2.Execute(wrongCaller, []string{"claude"}, request(input), options)
			var unavailable claudereplay.UnavailableError
			if !errors.As(err, &unavailable) || before != calls.Load() {
				t.Fatalf("foreign authenticated caller dispatched or did not get a scoped replay error: %v", err)
			}
			changed := strings.Replace(input, user, `{"role":"user","content":"edited"}`, 1)
			_, err = m2.Execute(ctx, []string{"claude"}, request(changed), options)
			if !errors.As(err, &unavailable) || before != calls.Load() {
				t.Fatalf("changed prefix dispatched or did not get a scoped replay error: %v", err)
			}
		})
	}
	revoked := cfgB.CloneForRuntime()
	revoked.AccessIsolation.Pools[0].Accounts = nil
	m2.PublishAccessPolicy(revoked)
	before := calls.Load()
	if _, err := m2.Execute(ctx, []string{"claude"}, request("["+user+"]"), options); err == nil || calls.Load() != before {
		t.Fatal("revoked account was dispatched by the second node")
	}
}

func TestClaudeResponsesReplayCollectorRequiresCompleteBlocks(t *testing.T) {
	start := []byte(`data: {"type":"message_start","message":{"id":"msg_fixture"}}`)
	thinking := []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`)
	sig := replayFixtureSignature("claude-opus-5-5-test")
	signed := []byte(fmt.Sprintf(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":%q}}`, sig))
	stopBlock := []byte(`data: {"type":"content_block_stop","index":0}`)
	stop := []byte(`data: {"type":"message_stop"}`)
	for _, frames := range [][][]byte{
		{start, thinking, signed, stop},
		{start, thinking, signed, stopBlock},
		{start, thinking, signed, stopBlock, signed, stop},
		{start, thinking, signed, stopBlock, []byte(`data: {"type":"error"}`), stop},
	} {
		collector := newClaudeResponsesReplayCollector()
		for _, frame := range frames {
			collector.observe(frame)
		}
		if _, complete := collector.content(); complete {
			t.Fatal("a failed or unfinished stream was eligible for replay")
		}
	}
	collector := newClaudeResponsesReplayCollector()
	for _, frame := range [][]byte{start, thinking, signed, stopBlock, stop} {
		collector.observe(frame)
	}
	if content, complete := collector.content(); !complete || gjson.GetBytes(content, "0.signature").String() != sig {
		t.Fatal("complete signed stream was not eligible for replay")
	}
}
