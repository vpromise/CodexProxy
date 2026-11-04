package responses

import (
	"github.com/tidwall/gjson"
	"strings"
	"testing"
)

func TestClaudeReplayDropsOnlyUnbackedSearchCitations(t *testing.T) {
	citation := `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Keep the answer","annotations":[{"type":"web_search_result_location","encrypted_index":"SEARCH_INDEX"},{"type":"page_location","encrypted_index":"DOCUMENT_INDEX"}]}]}`
	search := `{"type":"web_search_call","id":"ws_srvtoolu_1","action":{"query":"fixture"},"status":"completed"%s}`
	for _, tc := range []struct {
		name, results string
		keep          bool
	}{
		{"missing", "", false}, {"null", `,"results":null`, false}, {"empty", `,"results":[]`, false},
		{"unusable missing encrypted content", `,"results":[{"url":"https://example.com"}]`, false},
		{"complete", `,"results":[{"url":"https://example.com","encrypted_content":"RESULT"}]`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			item := strings.Replace(search, "%s", tc.results, 1)
			input := []byte(`{"input":[{"role":"user","content":"question"},` + item + `,` + citation + `,{"role":"user","content":"continue"}]}`)
			out, err := ConvertOpenAIResponsesRequestToClaudeChecked("claude-opus-5-5", input, false)
			if err != nil {
				t.Fatal(err)
			}
			s := string(out)
			if strings.Contains(s, "SEARCH_INDEX") != tc.keep {
				t.Fatalf("search citation preservation=%t want %t: %s", strings.Contains(s, "SEARCH_INDEX"), tc.keep, out)
			}
			for _, v := range []string{"Keep the answer", "DOCUMENT_INDEX", "srvtoolu_1"} {
				if !strings.Contains(s, v) {
					t.Fatalf("lost %q: %s", v, out)
				}
			}
			if tc.keep && !strings.Contains(s, "RESULT") {
				t.Fatalf("lost results: %s", out)
			}
		})
	}
}

func TestClaudeReplayCitationScopeAcrossToolTurns(t *testing.T) {
	text := func(id string) string {
		return `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer ` + id + `","annotations":[{"type":"web_search_result_location","encrypted_index":"` + id + `"}]}]}`
	}
	input := []byte(`{"input":[{"role":"user","content":"question"},` + text("BEFORE") + `,{"type":"function_call","call_id":"call_1","name":"fixture","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"},{"type":"web_search_call","id":"ws_srvtoolu_1"},` + text("AFTER") + `,{"role":"user","content":"new question"},` + text("NEXT_TURN") + `,{"role":"user","content":"continue"}]}`)
	out, err := ConvertOpenAIResponsesRequestToClaudeChecked("claude-opus-5-5", input, false)
	if err != nil {
		t.Fatal(err)
	}
	var indexes []string
	for _, m := range gjson.GetBytes(out, "messages").Array() {
		for _, b := range m.Get("content").Array() {
			for _, c := range b.Get("citations").Array() {
				indexes = append(indexes, c.Get("encrypted_index").String())
			}
		}
	}
	if strings.Join(indexes, ",") != "NEXT_TURN" {
		t.Fatalf("citation indexes=%v want NEXT_TURN; body=%s", indexes, out)
	}
	for _, v := range []string{"answer BEFORE", "answer AFTER", "answer NEXT_TURN", "call_1"} {
		if !strings.Contains(string(out), v) {
			t.Fatalf("lost %q: %s", v, out)
		}
	}
}
