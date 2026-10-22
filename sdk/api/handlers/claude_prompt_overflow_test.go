package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSyncOctober23PromptOverflowCode(t *testing.T) {
	for _, code := range []string{"", `,"code":null`, `,"code":""`, `,"code":"explicit"`, `,"code":123`} {
		detail := `{"type":"invalid_request_error","message":"prompt is too long: 210000 > 200000"` + code + `}`
		want := "context_length_exceeded"
		if code == `,"code":"explicit"` {
			want = "explicit"
		}
		if code == `,"code":123` {
			want = "123"
		}
		for _, body := range []string{`{"error":` + detail + `}`, `{"response":{"error":` + detail + `}}`} {
			out := BuildOpenAIResponsesStreamFailedChunk(http.StatusBadRequest, body, 7)
			if gjson.GetBytes(out, "response.error.code").String() != want || gjson.GetBytes(out, "type").String() != "response.failed" {
				t.Fatalf("failed=%s", out)
			}
			if gjson.Get(body, "error").Exists() {
				out = BuildOpenAIResponsesStreamErrorChunk(http.StatusBadRequest, body, 7)
				if gjson.GetBytes(out, "code").String() != want || gjson.GetBytes(out, "error").Exists() {
					t.Fatalf("legacy=%s", out)
				}
			}
		}
	}
	for _, typ := range []string{"server_error", "invalid_request_error"} {
		for _, message := range []string{"unrelated", "prompt is too long: 2 > 1"} {
			if typ == "invalid_request_error" && message != "unrelated" {
				continue
			}
			b, _ := json.Marshal(map[string]any{"error": map[string]any{"type": typ, "message": message}})
			out := BuildOpenAIResponsesStreamFailedChunk(400, string(b), 0)
			if gjson.GetBytes(out, "response.error.code").Exists() {
				t.Fatal(fmt.Sprintf("overclassified: %s", out))
			}
		}
	}
}
