package claude

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCodexClaudeMessageItemsKeepDoneOnlyText(t *testing.T) {
	for _, streamedFirst := range []bool{false, true} {
		for _, secondAdded := range []bool{false, true} {
			t.Run(fmt.Sprintf("streamed=%v/added=%v", streamedFirst, secondAdded), func(t *testing.T) {
				chunks := []string{`{"type":"response.created","response":{"id":"r","model":"gpt-5"}}`}
				if streamedFirst {
					chunks = append(chunks,
						`{"type":"response.output_item.added","item":{"type":"message","phase":"commentary"}}`,
						`{"type":"response.output_text.delta","delta":"First."}`)
				}
				chunks = append(chunks, `{"type":"response.output_item.done","item":{"type":"message","phase":"commentary","content":[{"type":"output_text","text":"First."}]}}`)
				if secondAdded {
					chunks = append(chunks, `{"type":"response.output_item.added","item":{"type":"message","phase":"final_answer"}}`)
				}
				chunks = append(chunks,
					`{"type":"response.output_item.done","item":{"type":"message","phase":"final_answer","content":[{"type":"output_text","text":"Second."}]}}`,
					`{"type":"response.output_item.done","item":{"type":"message","content":[{"type":"output_text","text":"Third."}]}}`,
					`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":2}}}`)
				var param any
				var texts []string
				var current strings.Builder
				open := false
				for _, chunk := range chunks {
					for _, out := range ConvertCodexResponseToClaude(context.Background(), "", nil, nil, []byte("data: "+chunk), &param) {
						for _, line := range strings.Split(string(out), "\n") {
							if !strings.HasPrefix(line, "data: ") {
								continue
							}
							event := gjson.Parse(strings.TrimPrefix(line, "data: "))
							switch event.Get("type").String() {
							case "content_block_start":
								if open {
									t.Fatal("overlapping content blocks")
								}
								if event.Get("content_block.type").String() != "text" {
									t.Fatal("message phase changed local text representation")
								}
								open = true
								current.Reset()
							case "content_block_delta":
								if event.Get("delta.type").String() == "text_delta" {
									current.WriteString(event.Get("delta.text").String())
								}
							case "content_block_stop":
								if !open {
									t.Fatal("stop without start")
								}
								texts = append(texts, current.String())
								open = false
							}
						}
					}
				}
				if open || !reflect.DeepEqual(texts, []string{"First.", "Second.", "Third."}) {
					t.Fatalf("text blocks = %q, open=%v", texts, open)
				}
			})
		}
	}
}
