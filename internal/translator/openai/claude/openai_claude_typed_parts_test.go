package claude

import (
	"context"
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestStreaming_DeltaContentTypedPartsArrayEmitsThinkingAndTextBlocks(t *testing.T) {
	events := runStream(t, streamReq,
		`{"choices":[{"index":0,"delta":{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":"The"}],"closed":true}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"index":0,"content":[{"type":"thinking","thinking":[{"type":"text","text":" done."}],"closed":true},{"type":"text","text":"hello"}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"index":0,"content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":19,"total_tokens":57,"completion_tokens":38,"completion_tokens_details":{"reasoning_tokens":35}}}`,
	)

	assertSequentialContentBlocks(t, events)

	var blockStarts []string
	var thinkingDeltas []string
	var textDeltas []string
	for _, e := range events {
		switch e.Type {
		case "content_block_start":
			blockStarts = append(blockStarts, gjson.Get(e.Payload, "content_block.type").String())
		case "content_block_delta":
			switch gjson.Get(e.Payload, "delta.type").String() {
			case "thinking_delta":
				thinkingDeltas = append(thinkingDeltas, gjson.Get(e.Payload, "delta.thinking").String())
			case "text_delta":
				textDeltas = append(textDeltas, gjson.Get(e.Payload, "delta.text").String())
			}
		}
	}

	if len(blockStarts) != 2 || blockStarts[0] != "thinking" || blockStarts[1] != "text" {
		t.Fatalf("expected block starts [thinking, text], got %v (events=%+v)", blockStarts, events)
	}
	if len(thinkingDeltas) != 2 || thinkingDeltas[0] != "The" || thinkingDeltas[1] != " done." {
		t.Fatalf("expected thinking deltas [The,  done.], got %v", thinkingDeltas)
	}
	if len(textDeltas) != 1 || textDeltas[0] != "hello" {
		t.Fatalf("expected text deltas [hello], got %v", textDeltas)
	}
}

func TestStreaming_DeltaContentTypedPartsArrayPlainThinkingAndUnknownPassthrough(t *testing.T) {
	events := runStream(t, streamReq,
		`{"choices":[{"index":0,"delta":{"content":[]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":"step 1"},{"type":"reasoning","text":" step 2"},{"type":"text","text":"answer: "},{"type":"custom_part","payload":"raw"}]},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	)

	assertSequentialContentBlocks(t, events)

	var thinkingDeltas []string
	var textDeltas []string
	for _, e := range events {
		if e.Type == "content_block_delta" {
			switch gjson.Get(e.Payload, "delta.type").String() {
			case "thinking_delta":
				thinkingDeltas = append(thinkingDeltas, gjson.Get(e.Payload, "delta.thinking").String())
			case "text_delta":
				textDeltas = append(textDeltas, gjson.Get(e.Payload, "delta.text").String())
			}
		}
	}

	if got := strings.Join(thinkingDeltas, ""); got != "step 1 step 2" {
		t.Fatalf("thinking text = %q, want %q (deltas=%v)", got, "step 1 step 2", thinkingDeltas)
	}
	if len(textDeltas) != 2 || textDeltas[0] != "answer: " || textDeltas[1] != `{"type":"custom_part","payload":"raw"}` {
		t.Fatalf("unexpected text deltas: %v", textDeltas)
	}
}

func TestNonStream_ContentPartsArrayThinkingAndBlockOrder(t *testing.T) {
	contentPartsResp := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"zai-glm-5-3","choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":[{"type":"text","text":"First "},{"type":"text","text":"thought."}],"closed":true},{"type":"text","text":"hello"}]},"finish_reason":"stop"}]}`)

	out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", nil, nil, contentPartsResp, nil)
	blocks := gjson.GetBytes(out, "content").Array()
	if len(blocks) != 2 {
		t.Fatalf("ConvertOpenAIResponseToClaudeNonStream expected 2 blocks, got %d (out=%s)", len(blocks), string(out))
	}
	if blocks[0].Get("type").String() != "thinking" || blocks[0].Get("thinking").String() != "First thought." {
		t.Fatalf("block[0] = %s, want thinking 'First thought.'", blocks[0].Raw)
	}
	if blocks[1].Get("type").String() != "text" || blocks[1].Get("text").String() != "hello" {
		t.Fatalf("block[1] = %s, want text 'hello'", blocks[1].Raw)
	}

	var paramAny any
	emitted := ConvertOpenAIResponseToClaude(context.Background(), "", []byte(`{"stream":false}`), nil, append([]byte("data: "), contentPartsResp...), &paramAny)
	if len(emitted) != 1 {
		t.Fatalf("expected 1 non-stream response from ConvertOpenAIResponseToClaude, got %d", len(emitted))
	}
	emittedBlocks := gjson.GetBytes(emitted[0], "content").Array()
	if len(emittedBlocks) != 2 || emittedBlocks[0].Get("type").String() != "thinking" || emittedBlocks[0].Get("thinking").String() != "First thought." || emittedBlocks[1].Get("type").String() != "text" || emittedBlocks[1].Get("text").String() != "hello" {
		t.Fatalf("convertOpenAINonStreamingToAnthropic blocks = %s, want [thinking, text]", string(emitted[0]))
	}

	// Verify top-level reasoning_content + string message.content emits [thinking, text] order
	topLevelReasoningResp := []byte(`{"id":"chatcmpl-2","object":"chat.completion","model":"deepseek","choices":[{"index":0,"message":{"role":"assistant","content":"Final answer","reasoning_content":"Step by step"},"finish_reason":"stop"}]}`)
	outTopLevel := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", nil, nil, topLevelReasoningResp, nil)
	topBlocks := gjson.GetBytes(outTopLevel, "content").Array()
	if len(topBlocks) != 2 || topBlocks[0].Get("type").String() != "thinking" || topBlocks[0].Get("thinking").String() != "Step by step" || topBlocks[1].Get("type").String() != "text" || topBlocks[1].Get("text").String() != "Final answer" {
		t.Fatalf("ConvertOpenAIResponseToClaudeNonStream block order = %s, want [thinking, text]", string(outTopLevel))
	}
}

func TestStreaming_TypedPartsWaitForOpenToolCall(t *testing.T) {
	events := runStream(t, streamReq,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`,
		`{"choices":[{"delta":{"content":[{"type":"thinking","thinking":"queued thought"},{"type":"text","text":"queued answer"}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	assertSequentialContentBlocks(t, events)
	var kinds, contents []string
	for _, e := range events {
		if e.Type == "content_block_start" {
			kinds = append(kinds, gjson.Get(e.Payload, "content_block.type").String())
		}
		if e.Type == "content_block_delta" {
			switch gjson.Get(e.Payload, "delta.type").String() {
			case "thinking_delta":
				contents = append(contents, gjson.Get(e.Payload, "delta.thinking").String())
			case "text_delta":
				contents = append(contents, gjson.Get(e.Payload, "delta.text").String())
			}
		}
	}
	if strings.Join(kinds, ",") != "tool_use,thinking,text" {
		t.Fatalf("block order: %v", kinds)
	}
	if strings.Join(contents, ",") != "queued thought,queued answer" {
		t.Fatalf("queued contents: %v", contents)
	}
}
