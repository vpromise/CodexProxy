package claude

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeImageURLSurvivesCodexTranslation(t *testing.T) {
	for _, compat := range []bool{false, true} {
		for _, scheme := range []string{"https", "http"} {
			t.Run(fmt.Sprintf("compat=%t/%s", compat, scheme), func(t *testing.T) {
				url := scheme + "://images.example/figure.png"
				raw := []byte(fmt.Sprintf(`{"model":"source","messages":[{"role":"user","content":[{"type":"text","text":"Describe"},{"type":"image","source":{"type":"url","url":" %s "}},{"type":"text","text":"precisely"}]}]}`, url))
				convert := ConvertClaudeRequestToCodex
				if compat {
					convert = ConvertClaudeRequestToCodexWithCompat
				}
				out := convert("target", raw, false)
				parts := gjson.GetBytes(out, "input.0.content").Array()
				if len(parts) != 3 {
					t.Fatalf("lost URL image: %s", out)
				}
				if parts[1].Get("type").String() != "input_image" || parts[1].Get("image_url").String() != url {
					t.Fatalf("image = %s", parts[1].Raw)
				}
				if parts[0].Get("text").String() != "Describe" || parts[2].Get("text").String() != "precisely" {
					t.Fatalf("changed content order: %s", out)
				}
			})
		}
	}
}
