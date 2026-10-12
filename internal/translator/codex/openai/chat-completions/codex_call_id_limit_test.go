package chat_completions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/tidwall/gjson"
)

// Codex rejects call_id values longer than 64 bytes, so over-length client
// IDs must be shortened consistently for calls and their outputs.
func TestOverLengthToolCallIDsAreShortenedConsistently(t *testing.T) {
	sharedPrefix := strings.Repeat("a", 70)
	longA := sharedPrefix + "_A"
	longB := sharedPrefix + "_B"
	input := []byte(`{
		"model": "gpt-6.1-sol",
		"messages": [
			{"role": "user", "content": "Multi-tool"},
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{"id": "` + longA + `", "type": "function", "function": {"name": "tool_a", "arguments": "{}"}},
					{"id": "` + longB + `", "type": "function", "function": {"name": "tool_b", "arguments": "{}"}},
					{"id": "short_id", "type": "function", "function": {"name": "tool_c", "arguments": "{}"}}
				]
			},
			{"role": "tool", "tool_call_id": "` + longA + `", "content": "res_a"},
			{"role": "tool", "tool_call_id": "` + longB + `", "content": "res_b"},
			{"role": "tool", "tool_call_id": "short_id", "content": "res_c"}
		]
	}`)

	out := ConvertOpenAIRequestToCodex("gpt-6.1-sol", input, true)

	var callIDs, outputIDs []string
	for _, item := range gjson.GetBytes(out, "input").Array() {
		switch item.Get("type").String() {
		case "function_call":
			callIDs = append(callIDs, item.Get("call_id").String())
		case "function_call_output":
			outputIDs = append(outputIDs, item.Get("call_id").String())
		}
	}
	if len(callIDs) != 3 || len(outputIDs) != 3 {
		t.Fatalf("expected 3 calls and 3 outputs, got %d and %d: %s", len(callIDs), len(outputIDs), out)
	}
	for i := range callIDs {
		if len(callIDs[i]) > 64 {
			t.Fatalf("call_id %d length = %d, want <= 64: %q", i, len(callIDs[i]), callIDs[i])
		}
		if callIDs[i] != outputIDs[i] {
			t.Fatalf("output %d call_id = %q, want %q", i, outputIDs[i], callIDs[i])
		}
	}
	if callIDs[0] == callIDs[1] {
		t.Fatalf("distinct over-length IDs collapsed to %q", callIDs[0])
	}
	if callIDs[2] != "short_id" {
		t.Fatalf("short call_id = %q, want unchanged", callIDs[2])
	}
}

// A legal 64-byte client ID may equal the shortened form of another
// over-length ID; results must still reach their own calls.
func TestShortenedToolCallIDCollisionWithLegalIDKeepsResultsMatched(t *testing.T) {
	longA := strings.Repeat("a", 70) + "_A"
	sum := sha256.Sum256([]byte(longA))
	idB := longA[:47] + "_" + hex.EncodeToString(sum[:8])
	if len(idB) != 64 || idB == longA {
		t.Fatalf("test setup: shortened ID = %q", idB)
	}
	input := []byte(`{
		"model": "gpt-6.1-sol",
		"messages": [
			{"role": "user", "content": "Multi-tool"},
			{
				"role": "assistant",
				"content": null,
				"tool_calls": [
					{"id": "` + longA + `", "type": "function", "function": {"name": "tool_a", "arguments": "{}"}},
					{"id": "` + idB + `", "type": "function", "function": {"name": "tool_b", "arguments": "{}"}}
				]
			},
			{"role": "tool", "tool_call_id": "` + idB + `", "content": "res_b"},
			{"role": "tool", "tool_call_id": "` + longA + `", "content": "res_a"}
		]
	}`)

	out := ConvertOpenAIRequestToCodex("gpt-6.1-sol", input, true)

	callIDByName := map[string]string{}
	outputIDByResult := map[string]string{}
	for _, item := range gjson.GetBytes(out, "input").Array() {
		switch item.Get("type").String() {
		case "function_call":
			callIDByName[item.Get("name").String()] = item.Get("call_id").String()
		case "function_call_output":
			outputIDByResult[item.Get("output").String()] = item.Get("call_id").String()
		}
	}
	if len(callIDByName) != 2 || len(outputIDByResult) != 2 {
		t.Fatalf("expected 2 calls and 2 outputs: %s", out)
	}
	if callIDByName["tool_a"] == callIDByName["tool_b"] {
		t.Fatalf("calls share call_id %q", callIDByName["tool_a"])
	}
	if callIDByName["tool_b"] != idB {
		t.Fatalf("legal call_id = %q, want unchanged %q", callIDByName["tool_b"], idB)
	}
	if outputIDByResult["res_a"] != callIDByName["tool_a"] || outputIDByResult["res_b"] != callIDByName["tool_b"] {
		t.Fatalf("results mismatched: calls=%v outputs=%v", callIDByName, outputIDByResult)
	}
}

func TestCodexCallIDLimitUnicodeAndBoundaries(t *testing.T) {
	for _, id := range []string{strings.Repeat("x", 64), strings.Repeat("x", 65), strings.Repeat("猫", 30), strings.Repeat("😀", 30)} {
		input, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "custom", "custom": map[string]any{"name": "edit", "input": "unchanged"}}}}, map[string]any{"role": "tool", "tool_call_id": id, "content": "result"}}})
		var first string
		for _, stream := range []bool{false, true} {
			out := ConvertOpenAIRequestToCodex("test-model", input, stream)
			if !json.Valid(out) || !utf8.Valid(out) {
				t.Fatalf("invalid JSON/UTF-8: %s", out)
			}
			items := gjson.GetBytes(out, "input").Array()
			if len(items) != 2 {
				t.Fatalf("expected paired custom call and output: %s", out)
			}
			got := items[0].Get("call_id").String()
			if len(got) > 64 || !utf8.ValidString(got) {
				t.Fatalf("invalid bounded ID %q", got)
			}
			if len(id) <= 64 && got != id {
				t.Fatalf("legal ID changed: %q", got)
			}
			if items[1].Get("call_id").String() != got || items[0].Get("type").String() != "custom_tool_call" || items[1].Get("output").String() != "result" || items[0].Get("input").String() != "unchanged" {
				t.Fatalf("pair or payload changed: %s", out)
			}
			if first != "" && first != got {
				t.Fatal("mapping differs by streaming mode")
			}
			first = got
		}
	}
}
