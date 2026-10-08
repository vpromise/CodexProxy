package claude

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeDocumentSurvivesChatTranslation(t *testing.T) {
	for _, compat := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("compat=%t/stream=%t", compat, stream), func(t *testing.T) {
				raw := []byte(`{"model":"source","messages":[{"role":"user","content":[{"type":"text","text":"Read this file"},{"type":"document","filename":"report.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}},{"type":"text","text":"Keep this order"}]}]}`)
				convert := ConvertClaudeRequestToOpenAI
				if compat {
					convert = ConvertClaudeRequestToOpenAIWithCompat
				}
				out := convert("target", raw, stream)
				parts := gjson.GetBytes(out, "messages.0.content").Array()
				if len(parts) != 3 {
					t.Fatalf("lost document: %s", out)
				}
				if parts[0].Get("text").String() != "Read this file" || parts[2].Get("text").String() != "Keep this order" {
					t.Fatalf("changed content order: %s", out)
				}
				if parts[1].Get("type").String() != "file" || parts[1].Get("file.filename").String() != "report.pdf" || parts[1].Get("file.file_data").String() != "data:application/pdf;base64,JVBERi0=" {
					t.Fatalf("changed document bytes or metadata: %s", parts[1].Raw)
				}
			})
		}
	}
}
