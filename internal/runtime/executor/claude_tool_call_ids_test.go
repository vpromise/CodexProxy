package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestClaudeToolCallIDsPreserveHistory(t *testing.T) {
	const rawID = "call|é/one"
	encoded := "cpa_tid_v1_" + base64.RawURLEncoding.EncodeToString([]byte(rawID))
	payload := []byte(fmt.Sprintf(`{ "messages": [{"role":"assistant","content":[{"type":"tool_use","id":%q,"name":"search","input":{"id":%q}},{"type":"tool_use","id":%q,"name":"search","input":{}}]}, {"role":"user","content":[{"type":"tool_result","tool_use_id":%q,"content":"call|é/one"},{"type":"tool_result","tool_use_id":%q,"content":"ok"}]}], "metadata":{"keep":"call|é/one"} }`, rawID, rawID, encoded, rawID, encoded))
	out := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(context.Background(), payload, "claude-opus-5-5")
	expected := bytes.ReplaceAll(payload, []byte(`"id":"`+rawID+`","name"`), []byte(`"id":"_`+encoded+`","name"`))
	expected = bytes.ReplaceAll(expected, []byte(`"tool_use_id":"`+rawID+`"`), []byte(`"tool_use_id":"_`+encoded+`"`))
	if !bytes.Equal(out, expected) {
		t.Fatalf("unexpected history repair:\ngot %s\nwant %s", out, expected)
	}
	if again := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(context.Background(), out, "claude-opus-5-5"); !bytes.Equal(again, out) {
		t.Fatalf("repair is not idempotent: %s", again)
	}
}

func TestClaudeToolCallIDsOnWire(t *testing.T) {
	for _, path := range []string{"execute", "stream", "count tokens"} {
		t.Run(path, func(t *testing.T) {
			upstream := &midSystemUpstream{}
			ctx := upstream.context(t, nil)
			auth := midSystemAuth()
			auth.Attributes["cloak_mode"] = "never"
			ex := NewClaudeExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"search"},{"role":"assistant","content":[{"type":"tool_use","id":"legacy|1","name":"search","input":{"q":"unchanged"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"legacy|1","content":"result unchanged"}]}]}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			switch path {
			case "execute":
				if _, err := ex.Execute(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
			case "stream":
				// Native Claude callers carry the stream flag in their request body.
				req.Payload = append(append([]byte(nil), req.Payload[:len(req.Payload)-1]...), []byte(`,"stream":true}`)...)
				result, err := ex.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			case "count tokens":
				if _, err := ex.CountTokens(ctx, auth, req, opts); err != nil {
					t.Fatal(err)
				}
			}
			id := gjson.GetBytes(upstream.body, "messages.1.content.0.id").String()
			paired := gjson.GetBytes(upstream.body, "messages.2.content.0.tool_use_id").String()
			if !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(id) || id != paired {
				t.Fatalf("invalid or unpaired IDs: %s", upstream.body)
			}
			if gjson.GetBytes(upstream.body, "messages.1.content.0.input.q").String() != "unchanged" || gjson.GetBytes(upstream.body, "messages.2.content.0.content").String() != "result unchanged" {
				t.Fatalf("non-ID history changed: %s", upstream.body)
			}
		})
	}
}
