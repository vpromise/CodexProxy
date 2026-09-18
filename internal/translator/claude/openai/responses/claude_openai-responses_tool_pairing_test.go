package responses

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRepairClaudeToolPairingPreservesValidHistory(t *testing.T) {
	messages := [][]byte{
		[]byte(`{"role":"user","content":"start"}`),
		[]byte(`{"role":"assistant","content":[{"type":"thinking","thinking":"thought","signature":"opaque"},{"type":"tool_use","id":"call_1","name":"tool","input":{}}]}`),
		[]byte(`{"role":"user","future":true,"content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"url","url":"https://example.invalid/image.png"}}],"cache_control":{"type":"ephemeral"}},{"type":"text","text":"continue"}]}`),
	}
	got := repairClaudeToolPairing(messages)
	if len(got) != len(messages) {
		t.Fatalf("valid history gained messages: %q", got)
	}
	for i := range messages {
		if !bytes.Equal(got[i], messages[i]) {
			t.Fatalf("valid message %d changed: %s", i, got[i])
		}
	}
}

func TestClaudeStandaloneToolOutputPreservesMedia(t *testing.T) {
	raw := []byte(`{"input":[{"type":"custom_tool_call_output","output":[{"type":"input_text","text":"task context"},{"type":"input_image","image_url":"https://example.invalid/image.png"},{"type":"input_file","file_data":"data:application/pdf;base64,cGRm"}]}]}`)
	got := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	parts := gjson.GetBytes(got, "messages.0.content")
	if parts.Get("0.text").String() != "task context" || parts.Get("1.source.url").String() != "https://example.invalid/image.png" || parts.Get("2.source.data").String() != "cGRm" {
		t.Fatalf("standalone task lost media or text: %s", got)
	}
}

// claudeMessageInvariantProblems reports shapes Anthropic rejects. It only
// describes; repairClaudeToolPairing already fixes the cases it knows about,
// so anything reported here is a new history shape worth investigating from
// the proxy log instead of from an upstream 400.
func claudeMessageInvariantProblems(messages [][]byte) []string {
	var problems []string
	if len(messages) > 0 && gjson.GetBytes(messages[0], "role").String() != "user" {
		problems = append(problems, "first message is not user")
	}
	for i, msg := range messages {
		role := gjson.GetBytes(msg, "role").String()
		content := gjson.GetBytes(msg, "content")
		switch role {
		case "assistant":
			var toolUseIDs []string
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_use" {
					toolUseIDs = append(toolUseIDs, block.Get("id").String())
				}
				return true
			})
			if len(toolUseIDs) == 0 {
				continue
			}
			if i+1 >= len(messages) || gjson.GetBytes(messages[i+1], "role").String() != "user" {
				problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_use has no following user message")
				continue
			}
			next := gjson.GetBytes(messages[i+1], "content")
			answered := map[string]struct{}{}
			next.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_result" {
					answered[block.Get("tool_use_id").String()] = struct{}{}
				}
				return true
			})
			for _, id := range toolUseIDs {
				if _, ok := answered[id]; !ok {
					problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_use "+id+" has no tool_result in messages["+strconv.Itoa(i+1)+"]")
				}
			}
		case "user":
			var previous gjson.Result
			if i > 0 {
				previous = gjson.GetBytes(messages[i-1], "content")
			}
			toolUses := map[string]struct{}{}
			previous.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_use" {
					toolUses[block.Get("id").String()] = struct{}{}
				}
				return true
			})
			leading := true
			content.ForEach(func(_, block gjson.Result) bool {
				if block.Get("type").String() == "tool_result" {
					if !leading {
						problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_result after non-tool_result block")
					}
					if _, ok := toolUses[block.Get("tool_use_id").String()]; !ok {
						problems = append(problems, "messages["+strconv.Itoa(i)+"] tool_result "+block.Get("tool_use_id").String()+" has no tool_use in the previous message")
					}
				} else {
					leading = false
				}
				return true
			})
		}
	}
	return problems
}

func TestConvertOpenAIResponsesRequestToClaude_StandaloneToolOutputBecomesUserText(t *testing.T) {
	// Codex create_thread seeds a new thread with a function_call_output whose
	// call_id never appears as a function_call in the same input. Claude
	// rejects tool_result blocks without a matching tool_use, so it must
	// degrade to text.
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{
				"type":"function_call_output",
				"call_id":"toolu_1789312108939888000_16",
				"output":"<codex_delegation>Launched from another task.</codex_delegation>"
			},
			{
				"type":"message",
				"role":"user",
				"content":[{"type":"input_text","text":"Reply with PROBE_OK."}]
			}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_result" {
				t.Fatalf("unexpected tool_result for standalone output. Output: %s", string(out))
			}
			return true
		})
		return true
	})
	if got := root.Get("messages.0.role").String(); got != "user" {
		t.Fatalf("messages.0.role = %q, want user. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.content.0.type").String(); got != "text" {
		t.Fatalf("messages.0.content.0.type = %q, want text. Output: %s", got, string(out))
	}
	if got := root.Get("messages.0.content.0.text").String(); got != "<codex_delegation>Launched from another task.</codex_delegation>" {
		t.Fatalf("messages.0.content.0.text = %q. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SynthesizesResultForDanglingToolUse(t *testing.T) {
	// A session that died mid-tool leaves a function_call with no output.
	// Anthropic requires a tool_result at the start of the next user message,
	// so one is synthesized.
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run the tests."}]},
			{"type":"function_call","call_id":"call_1","name":"shell","arguments":"{\"cmd\":\"npm test\"}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Session was restarted; carry on."}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.#").Int(); got != 3 {
		t.Fatalf("message count = %d, want 3. Output: %s", got, string(out))
	}
	if got := root.Get("messages.1.content.0.type").String(); got != "tool_use" {
		t.Fatalf("messages.1.content.0.type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.2.content.0.type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.tool_use_id").String(); got != "call_1" {
		t.Fatalf("synthesized tool_use_id = %q, want call_1. Output: %s", got, string(out))
	}
	if !root.Get("messages.2.content.0.is_error").Bool() {
		t.Fatalf("synthesized tool_result should be is_error. Output: %s", string(out))
	}
	if got := root.Get("messages.2.content.1.text").String(); got != "Session was restarted; carry on." {
		t.Fatalf("messages.2.content.1.text = %q, want 'Session was restarted; carry on.'. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SynthesizesResultForTrailingToolUse(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run ls."}]},
			{"type":"function_call","call_id":"call_tail","name":"exec","arguments":"{}"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.#").Int(); got != 3 {
		t.Fatalf("message count = %d, want 3. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want user. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.2.content.0.type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.tool_use_id").String(); got != "call_tail" {
		t.Fatalf("messages.2.content.0.tool_use_id = %q, want call_tail. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_MovesToolResultsAheadOfInjectedText(t *testing.T) {
	// A heartbeat seed can land between a tool call and its output. Anthropic
	// requires tool_result blocks to lead the user message.
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run ls."}]},
			{"type":"function_call","call_id":"call_hb","name":"exec","arguments":"{}"},
			{"type":"function_call_output","id":"fco_seed","name":"automation_update","namespace":"codex_app","output":"<heartbeat>tick</heartbeat>"},
			{"type":"function_call_output","call_id":"call_hb","output":"a.txt"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Continue."}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.#").Int(); got != 3 {
		t.Fatalf("message count = %d, want 3. Output: %s", got, string(out))
	}
	blocks := root.Get("messages.2.content").Array()
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks in messages.2, got %d. Output: %s", len(blocks), string(out))
	}
	if got := blocks[0].Get("type").String(); got != "tool_result" || blocks[0].Get("tool_use_id").String() != "call_hb" {
		t.Fatalf("blocks[0] = %s, want tool_result for call_hb. Output: %s", blocks[0].Raw, string(out))
	}
	if got := blocks[0].Get("content").String(); got != "a.txt" {
		t.Fatalf("blocks[0].content = %q, want a.txt", got)
	}
	if got := blocks[1].Get("text").String(); got != "<heartbeat>tick</heartbeat>" {
		t.Fatalf("blocks[1].text = %q, want heartbeat text. Output: %s", got, string(out))
	}
	if got := blocks[2].Get("text").String(); got != "Continue." {
		t.Fatalf("blocks[2].text = %q, want Continue. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SynthesizesResultForTrailingToolUseOnPrefillRejectingModel(t *testing.T) {
	// Models that reject assistant prefill (fable/opus-5/sonnet-4-6) used to
	// drop the trailing tool_use before repair could answer it, losing the
	// call history entirely. The synthesized tool_result must survive instead.
	raw := []byte(`{
		"model":"claude-fable-5-1",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run ls."}]},
			{"type":"function_call","call_id":"call_tail","name":"exec","arguments":"{}"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-fable-5-1", raw, false)
	root := gjson.ParseBytes(out)

	if got := root.Get("messages.#").Int(); got != 3 {
		t.Fatalf("message count = %d, want 3. Output: %s", got, string(out))
	}
	if got := root.Get("messages.1.content.0.type").String(); got != "tool_use" {
		t.Fatalf("messages.1.content.0.type = %q, want tool_use. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.role").String(); got != "user" {
		t.Fatalf("messages.2.role = %q, want user. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.2.content.0.type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.tool_use_id").String(); got != "call_tail" {
		t.Fatalf("messages.2.content.0.tool_use_id = %q, want call_tail. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_LateOrphanToolResultFoldsToText(t *testing.T) {
	// An output can pass the emittedToolUses gate but still land after an
	// intervening assistant message, where its tool_result no longer answers
	// the immediately preceding tool_use. The repair must fold it into text
	// and synthesize the missing answer for the real dangling call.
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run ls."}]},
			{"type":"function_call","call_id":"call_a","name":"exec","arguments":"{}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"wait"}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"interlude"}]},
			{"type":"function_call_output","call_id":"call_a","output":"a.txt"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	// messages: user | assistant(tool_use call_a) | user | assistant(text) | user
	if got := root.Get("messages.#").Int(); got != 5 {
		t.Fatalf("message count = %d, want 5. Output: %s", got, string(out))
	}
	// The user message right after the dangling tool_use must lead with a
	// synthesized error tool_result for call_a.
	if got := root.Get("messages.2.content.0.type").String(); got != "tool_result" {
		t.Fatalf("messages.2.content.0.type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := root.Get("messages.2.content.0.tool_use_id").String(); got != "call_a" {
		t.Fatalf("messages.2.content.0.tool_use_id = %q, want call_a. Output: %s", got, string(out))
	}
	if !root.Get("messages.2.content.0.is_error").Bool() {
		t.Fatalf("synthesized tool_result should be is_error. Output: %s", string(out))
	}
	// The late real output must not remain a tool_result after the interlude
	// assistant message; it folds into plain text.
	last := root.Get("messages.4")
	if got := last.Get("role").String(); got != "user" {
		t.Fatalf("messages.4.role = %q, want user. Output: %s", got, string(out))
	}
	last.Get("content").ForEach(func(_, block gjson.Result) bool {
		if block.Get("type").String() == "tool_result" {
			t.Fatalf("late output must not remain a tool_result. Output: %s", string(out))
		}
		return true
	})
	if got := last.Get("content.0.text").String(); got != "a.txt" {
		t.Fatalf("messages.4.content.0.text = %q, want a.txt. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_EmptyStandaloneToolOutputKeepsMarker(t *testing.T) {
	// A standalone output with empty content must still produce a non-empty
	// user message; Anthropic rejects empty content arrays too. Both the
	// string form and the structured array form with an empty text part
	// collapse to the marker text.
	for _, output := range []string{`""`, `[{"type":"input_text","text":""}]`} {
		raw := []byte(`{
			"model":"claude-test",
			"input":[
				{"type":"function_call_output","call_id":"orphan","output":` + output + `}
			]
		}`)

		out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
		root := gjson.ParseBytes(out)

		if got := root.Get("messages.#").Int(); got != 1 {
			t.Fatalf("message count = %d, want 1 for output %s. Output: %s", got, output, string(out))
		}
		content := root.Get("messages.0.content")
		if content.IsArray() {
			if len(content.Array()) == 0 {
				t.Fatalf("empty content array in messages.0 for output %s. Output: %s", output, string(out))
			}
			if got := content.Get("0.type").String(); got != "text" {
				t.Fatalf("messages.0.content.0.type = %q, want text for output %s. Output: %s", got, output, string(out))
			}
			if got := content.Get("0.text").String(); strings.TrimSpace(got) == "" {
				t.Fatalf("empty text block in messages.0 for output %s. Output: %s", output, string(out))
			}
			continue
		}
		if got := content.String(); strings.TrimSpace(got) == "" {
			t.Fatalf("empty string content in messages.0 for output %s. Output: %s", output, string(out))
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_SanitizedIDCollisionKeepsOrphanAsText(t *testing.T) {
	// "call.custom:1" and "call_custom_1" sanitize to the same Claude id, but
	// they are distinct calls. The unpaired raw id must degrade to text while
	// the real pairing stays a tool_result.
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run."}]},
			{"type":"function_call","call_id":"call.custom:1","name":"exec","arguments":"{}"},
			{"type":"function_call_output","call_id":"call_custom_1","output":"unrelated context"},
			{"type":"function_call_output","call_id":"call.custom:1","output":"real result"}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	blocks := root.Get("messages.2.content").Array()
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks in messages.2, got %d. Output: %s", len(blocks), string(out))
	}
	if got := blocks[0].Get("type").String(); got != "tool_result" {
		t.Fatalf("blocks[0].type = %q, want tool_result. Output: %s", got, string(out))
	}
	if got := blocks[0].Get("content").String(); got != "real result" {
		t.Fatalf("blocks[0].content = %q, want 'real result'. Output: %s", got, string(out))
	}
	if got := blocks[1].Get("type").String(); got != "text" {
		t.Fatalf("blocks[1].type = %q, want text. Output: %s", got, string(out))
	}
	if got := blocks[1].Get("text").String(); got != "unrelated context" {
		t.Fatalf("blocks[1].text = %q, want 'unrelated context'. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_EmptyLateOrphanToolResultKeepsNonEmptyUserMessage(t *testing.T) {
	// An empty late orphan output folds to nothing; the user message must
	// still carry a marker instead of degenerating to content:[] which
	// Anthropic also rejects.
	raw := []byte(`{
		"model":"claude-fable-5-1",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run the tool."}]},
			{"type":"function_call","call_id":"call_a","name":"exec","arguments":"{}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Wait."}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Interlude."}]},
			{"type":"function_call_output","call_id":"call_a","output":""}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-fable-5-1", raw, false)
	root := gjson.ParseBytes(out)

	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		content := msg.Get("content")
		if content.IsArray() && len(content.Array()) == 0 {
			t.Fatalf("empty content array in message. Output: %s", string(out))
		}
		content.ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "tool_result" && block.Get("tool_use_id").String() == "call_a" && !block.Get("is_error").Bool() {
				t.Fatalf("late empty orphan must not remain a tool_result. Output: %s", string(out))
			}
			return true
		})
		return true
	})
	last := root.Get("messages.4")
	if got := last.Get("role").String(); got != "user" {
		t.Fatalf("messages.4.role = %q, want user. Output: %s", got, string(out))
	}
	if got := last.Get("content.0.text").String(); got != "Tool result was empty." {
		t.Fatalf("messages.4.content.0.text = %q, want marker text. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_LateOrphanToolResultArrayOfEmptyTextKeepsMarker(t *testing.T) {
	// A late orphan result whose content is an array of empty text blocks
	// must not emit empty text blocks; the whole result folds to the marker.
	raw := []byte(`{
		"model":"claude-fable-5-1",
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Run."}]},
			{"type":"function_call","call_id":"a","name":"exec","arguments":"{}"},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"Wait."}]},
			{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Interlude."}]},
			{"type":"function_call_output","call_id":"a","output":[{"type":"input_text","text":""},{"type":"input_text","text":""}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-fable-5-1", raw, false)
	root := gjson.ParseBytes(out)

	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" && strings.TrimSpace(block.Get("text").String()) == "" {
				t.Fatalf("empty text block emitted. Output: %s", string(out))
			}
			if block.Get("type").String() == "tool_result" && block.Get("tool_use_id").String() == "a" && !block.Get("is_error").Bool() {
				t.Fatalf("late orphan must not remain a tool_result. Output: %s", string(out))
			}
			return true
		})
		return true
	})
	last := root.Get("messages.4")
	if got := last.Get("content.0.text").String(); got != "Tool result was empty." {
		t.Fatalf("messages.4.content.0.text = %q, want marker text. Output: %s", got, string(out))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_StandaloneToolOutputDropsEmptyTextInMixedArray(t *testing.T) {
	// A standalone output whose array mixes an empty text part with a real
	// one must not carry the empty block into the user message.
	raw := []byte(`{
		"model":"claude-test",
		"input":[
			{"type":"function_call_output","call_id":"orphan","output":[{"type":"input_text","text":""},{"type":"input_text","text":"context"}]}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	root := gjson.ParseBytes(out)

	root.Get("messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("type").String() == "text" && strings.TrimSpace(block.Get("text").String()) == "" {
				t.Fatalf("empty text block emitted. Output: %s", string(out))
			}
			if block.Get("type").String() == "tool_result" {
				t.Fatalf("standalone output must not remain a tool_result. Output: %s", string(out))
			}
			return true
		})
		return true
	})
	if got := root.Get("messages.0.content").String(); got != "context" {
		t.Fatalf("messages.0.content = %q, want 'context'. Output: %s", got, string(out))
	}
}

func TestClaudeMessageInvariantProblems(t *testing.T) {
	msg := func(raw string) []byte { return []byte(raw) }
	clean := [][]byte{
		msg(`{"role":"user","content":"hi"}`),
		msg(`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"exec","input":{}}]}`),
		msg(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"},{"type":"text","text":"go on"}]}`),
	}
	if got := claudeMessageInvariantProblems(clean); len(got) != 0 {
		t.Fatalf("clean history reported problems: %v", got)
	}
	broken := [][]byte{
		msg(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t0","content":"orphan"}]}`),
		msg(`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"exec","input":{}}]}`),
		msg(`{"role":"user","content":[{"type":"text","text":"x"},{"type":"tool_result","tool_use_id":"t2","content":"ok"}]}`),
	}
	got := claudeMessageInvariantProblems(broken)
	want := []string{"tool_result t0 has no tool_use", "tool_result after non-tool_result block", "tool_use t1 has no tool_result", "tool_result t2 has no tool_use"}
	for _, fragment := range want {
		found := false
		for _, problem := range got {
			if strings.Contains(problem, fragment) {
				found = true
			}
		}
		if !found {
			t.Fatalf("expected a problem containing %q, got %v", fragment, got)
		}
	}
	firstNotUser := [][]byte{
		msg(`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"exec","input":{}}]}`),
	}
	got = claudeMessageInvariantProblems(firstNotUser)
	if len(got) == 0 || !strings.Contains(got[0], "first message is not user") {
		t.Fatalf("expected first-message problem, got %v", got)
	}
}
