package responses

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestClaudeResponsesCachedToolMetadataMatchesLegacy(t *testing.T) {
	requests := []string{
		`{}`, `{`, `null`, `[]`,
		`{"tools":[{"type":"custom","name":"editor.apply"},{"type":"function","name":"editor_apply"}],"input":[{"type":"function_call","name":"history.tool","namespace":"history"}]}`,
		`{"tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"read"},{"type":"custom","name":"write"}]},{"type":"function","name":"ns__read"}]}`,
		`{"tools":{"type":"function","name":"read"},"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"ns","tools":[{"type":"custom","name":"write"}]}]}]}`,
	}
	for i := 0; i < 200; i++ {
		requests = append(requests, fmt.Sprintf(`{"tools":[{"type":"namespace","name":"ns%d","tools":[{"type":"custom","name":"write"},{"type":"function","name":"read"}]},{"type":"function","name":%q}],"input":[{"type":"function_call","name":%q,"namespace":"history"}]}`, i%7, strings.Repeat("long", 20)+fmt.Sprint(i), fmt.Sprintf("old.tool.%d", i)))
	}
	for i, request := range requests {
		raw := []byte(request)
		metadata := newClaudeResponsesToolMetadata(raw)
		if !reflect.DeepEqual(metadata.customNames, responsesCustomToolNames(raw)) {
			t.Fatalf("case %d: custom names differ", i)
		}
		names := []string{"", " ", "read", "write", "ns__read", "ns__write", "editor.apply", "editor_apply", "history.tool", "unknown"}
		for short := range metadata.names.fromClaude {
			names = append(names, short)
		}
		for name := range metadata.winners {
			names = append(names, name)
		}
		for _, name := range names {
			for _, path := range []string{"", "item"} {
				item := []byte(`{"type":"function_call","name":"before","namespace":"old","item":{"type":"function_call","name":"before","namespace":"old"}}`)
				got := metadata.applyNamespace(item, name, path)
				want := applyResponsesFunctionCallNamespaceFields(item, raw, name, path)
				if !bytes.Equal(got, want) {
					t.Fatalf("case %d name %q path %q: got %s want %s", i, name, path, got, want)
				}
			}
		}
	}
}

func BenchmarkClaudeResponsesToolMetadata(b *testing.B) {
	request := []byte(fmt.Sprintf(`{"tools":[{"type":"namespace","name":"ns","tools":[{"type":"function","name":"read"}]}],"input":[{"type":"message","role":"user","content":%q}]}`, strings.Repeat("context ", 81920)))
	item := []byte(`{"type":"function_call","name":""}`)
	for _, cached := range []bool{false, true} {
		b.Run(fmt.Sprint("cached=", cached), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if cached {
					metadata := newClaudeResponsesToolMetadata(request)
					for j := 0; j < 8; j++ {
						metadata.applyNamespace(item, "ns__read", "")
					}
				} else {
					for j := 0; j < 8; j++ {
						applyResponsesFunctionCallNamespaceFields(item, request, "ns__read", "")
					}
				}
			}
		})
	}
}
