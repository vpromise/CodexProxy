package helps

import (
	"context"
	"net/http"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexIntegerSchemaTranslationPolicy(t *testing.T) {
	input := []byte(`{"model":"fixture","input":"hello","tools":[{"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"yield_time_ms":{"type":"number"},"ratio":{"type":"number"}}}}]}`)
	for _, tt := range []struct {
		name, executor, ua, want string
		target                   sdktranslator.Format
	}{
		{"Claude", "claude", "codex-tui/0.154.0", "integer", sdktranslator.FormatClaude},
		{"Chat compatibility", "custom-provider", "codex-tui/0.154.0", "integer", sdktranslator.FormatOpenAI},
		{"Codex protocol on third party", "custom-provider", "codex-tui/0.154.0", "integer", sdktranslator.FormatCodex},
		{"native Codex", "codex", "codex-tui/0.154.0", "number", sdktranslator.FormatCodex},
		{"native websocket", "codex-websockets", "codex-tui/0.154.0", "number", sdktranslator.FormatCodex},
		{"no executor identity", "", "codex-tui/0.154.0", "number", sdktranslator.FormatCodex},
		{"other caller", "claude", "other-codex-wrapper/1", "number", sdktranslator.FormatClaude},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{"User-Agent": {tt.ua}}
			for _, compat := range []bool{false, true} {
				original, working := TranslateRequestPairWithAPIKeyModelCompatibility(context.Background(), headers, nil, sdktranslator.FormatOpenAIResponse, tt.target, "fixture", input, input, false, compat, tt.executor)
				path := "tools.0.parameters.properties."
				if tt.target == sdktranslator.FormatClaude {
					path = "tools.0.input_schema.properties."
				}
				if tt.target == sdktranslator.FormatOpenAI {
					path = "tools.0.function.parameters.properties."
				}
				for _, body := range [][]byte{original, working} {
					if got := gjson.GetBytes(body, path+"yield_time_ms.type").String(); got != tt.want {
						t.Fatalf("compat=%v: type=%q, want %q; body=%s", compat, got, tt.want, body)
					}
					if got := gjson.GetBytes(body, path+"ratio.type").String(); got != "number" {
						t.Fatalf("unlisted numeric field changed: %s", body)
					}
				}
				if len(original) > 0 && len(working) > 0 && &original[0] == &working[0] {
					t.Fatal("translated baseline and working payload share storage")
				}
			}
			if got := gjson.GetBytes(input, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
				t.Fatal("original caller payload mutated")
			}
		})
	}
}
