package claude

import (
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestStreaming_WhitespaceReasoningNeverOpensThinkingBlock(t *testing.T) {
	toolCall := `{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Read","arguments":"{}"}}]}`
	tests := []struct {
		name   string
		deltas []string
		want   []string
	}{
		{"whitespace only then text", []string{`{"reasoning_content":" "}`, `{"content":"Hi"}`}, []string{"text:Hi"}},
		{"whitespace inside a thought", []string{`{"reasoning_content":"a"}`, `{"reasoning_content":"\n\n"}`, `{"reasoning_content":"b"}`}, []string{"thinking:a\n\nb"}},
		{"whitespace after text closed the thought", []string{`{"reasoning_content":"a"}`, `{"content":"x"}`, `{"reasoning_content":" "}`, `{"content":"y"}`}, []string{"thinking:a", "text:xy"}},
		{"whitespace only while a tool call is open", []string{toolCall, `{"reasoning_content":" "}`}, []string{"tool_use:"}},
		{"whitespace inside a thought while a tool call is open", []string{toolCall, `{"reasoning_content":"a"}`, `{"reasoning_content":" "}`, `{"reasoning_content":"b"}`}, []string{"tool_use:", "thinking:a b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var chunks []string
			for _, delta := range tt.deltas {
				chunks = append(chunks, `{"id":"c1","model":"m","choices":[{"index":0,"delta":`+delta+`,"finish_reason":null}]}`)
			}
			chunks = append(chunks, `{"id":"c1","model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
			events := runStream(t, streamReq, chunks...)
			assertSequentialContentBlocks(t, events)

			var got []string
			for _, e := range events {
				switch e.Type {
				case "content_block_start":
					got = append(got, gjson.Get(e.Payload, "content_block.type").String()+":")
				case "content_block_delta":
					got[len(got)-1] += gjson.Get(e.Payload, "delta.thinking").String() + gjson.Get(e.Payload, "delta.text").String()
				}
			}
			if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tt.want) {
				t.Fatalf("blocks = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNonStream_WhitespaceReasoningNeverOpensThinkingBlock(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    []string
	}{
		{"whitespace only reasoning_content", `{"role":"assistant","content":"Hi","reasoning_content":" "}`, []string{"text:Hi"}},
		{"whitespace only thinking part", `{"role":"assistant","content":[{"type":"thinking","thinking":" "},{"type":"text","text":"Hi"}]}`, []string{"text:Hi"}},
		{"whitespace inside thinking parts", `{"role":"assistant","content":[{"type":"thinking","thinking":"a"},{"type":"thinking","thinking":" "},{"type":"thinking","thinking":"b"},{"type":"text","text":"Hi"}]}`, []string{"thinking:a b", "text:Hi"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := []byte(`{"id":"c1","model":"m","choices":[{"index":0,"message":` + tt.message + `,"finish_reason":"stop"}]}`)
			var paramAny any
			outputs := [][]byte{
				ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", nil, nil, resp, nil),
				ConvertOpenAIResponseToClaude(context.Background(), "", []byte(`{"stream":false}`), nil, append([]byte("data: "), resp...), &paramAny)[0],
			}
			for i, out := range outputs {
				var got []string
				for _, block := range gjson.GetBytes(out, "content").Array() {
					got = append(got, block.Get("type").String()+":"+block.Get("thinking").String()+block.Get("text").String())
				}
				if fmt.Sprintf("%q", got) != fmt.Sprintf("%q", tt.want) {
					t.Fatalf("outputs[%d] blocks = %q, want %q (out=%s)", i, got, tt.want, out)
				}
			}
		})
	}
}
