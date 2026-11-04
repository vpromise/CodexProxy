package helps

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeCodexInputItemIDsSanitizesInvalidCharacters(t *testing.T) {
	overlongPlusSlash := "msg_" + strings.Repeat("A", 30) + "+/" + strings.Repeat("A", 414)
	overlongSlashPlus := "msg_" + strings.Repeat("A", 30) + "/+" + strings.Repeat("A", 414)
	overlongUnprefixedPlusSlash := strings.Repeat("A", 30) + "+/" + strings.Repeat("A", 414)
	overlongTailPlus := "msg_" + strings.Repeat("B", 100) + "+" + strings.Repeat("B", 100)
	overlongTailSlash := "msg_" + strings.Repeat("B", 100) + "/" + strings.Repeat("B", 100)

	body := []byte(`{"input":[` +
		`{"type":"message","role":"assistant","id":"` + overlongPlusSlash + `","content":[{"type":"output_text","text":"Hello"}]},` +
		`{"type":"message","role":"assistant","id":"` + overlongSlashPlus + `","content":[{"type":"output_text","text":"World"}]},` +
		`{"type":"message","role":"assistant","id":"` + overlongUnprefixedPlusSlash + `","content":[{"type":"output_text","text":"Unprefixed"}]},` +
		`{"type":"message","role":"assistant","id":"` + overlongTailPlus + `","content":[{"type":"output_text","text":"Tail plus"}]},` +
		`{"type":"message","role":"assistant","id":"` + overlongTailSlash + `","content":[{"type":"output_text","text":"Tail slash"}]},` +
		`{"type":"message","role":"assistant","id":"msg_short+id/with=chars","content":[{"type":"output_text","text":"Short"}]},` +
		`{"type":"message","role":"assistant","id":"msg_short_id_with_chars","content":[{"type":"output_text","text":"Short existing"}]},` +
		`{"type":"function_call","id":"call:0+1/2","call_id":"call-1","name":"lookup","arguments":"{}"},` +
		`{"type":"function_call_output","id":"fco:0+1/2","call_id":"call-1","output":"ok"},` +
		`{"type":"tool_search_call","id":"ts_call+1/2","call_id":"call-2","execution":"client"}` +
		`]}`)

	first := SanitizeCodexInputItemIDs(body)
	second := SanitizeCodexInputItemIDs(body)
	normalizedAgain := SanitizeCodexInputItemIDs(first)

	if string(first) != string(second) {
		t.Fatalf("invalid character normalization is not deterministic: first=%s second=%s", first, second)
	}
	if string(first) != string(normalizedAgain) {
		t.Fatalf("invalid character normalization is not idempotent: first=%s normalized_again=%s", first, normalizedAgain)
	}

	items := gjson.GetBytes(first, "input").Array()
	if len(items) != 10 {
		t.Fatalf("input length = %d, want 10", len(items))
	}

	seen := make(map[string]int, len(items))
	expectedPrefixes := []string{"msg_", "msg_", "msg_", "msg_", "msg_", "msg_", "msg_", "fc_", "fco_", "tsc_"}
	for idx, item := range items {
		actual := item.Get("id").String()
		if !isPortableToolCallID(actual) {
			t.Fatalf("input.%d.id = %q, contains invalid Codex ID characters", idx, actual)
		}
		if len([]rune(actual)) > codexInputItemIDLimit {
			t.Fatalf("input.%d.id length = %d, want at most %d: %q", idx, len([]rune(actual)), codexInputItemIDLimit, actual)
		}
		if !strings.HasPrefix(actual, expectedPrefixes[idx]) {
			t.Fatalf("input.%d.id = %q, want prefix %q", idx, actual, expectedPrefixes[idx])
		}
		if prevIdx, exists := seen[actual]; exists {
			t.Fatalf("input.%d.id collided with input.%d.id: %q", idx, prevIdx, actual)
		}
		seen[actual] = idx
	}
	if got := items[6].Get("id").String(); got != "msg_short_id_with_chars" {
		t.Fatalf("existing valid ID changed: got %q, want msg_short_id_with_chars", got)
	}

	repeatedBody := []byte(`{"input":[` +
		`{"type":"message","role":"assistant","id":"` + overlongPlusSlash + `"},` +
		`{"type":"message","role":"assistant","id":"` + overlongPlusSlash + `"},` +
		`{"type":"message","role":"assistant","id":"msg_short+repeat/id"},` +
		`{"type":"message","role":"assistant","id":"msg_short+repeat/id"}` +
		`]}`)
	repeatedGot := SanitizeCodexInputItemIDs(repeatedBody)
	if id0, id1 := gjson.GetBytes(repeatedGot, "input.0.id").String(), gjson.GetBytes(repeatedGot, "input.1.id").String(); id0 != id1 || !isPortableToolCallID(id0) {
		t.Fatalf("repeated overlong invalid IDs did not map stably: %q vs %q", id0, id1)
	}
	if id2, id3 := gjson.GetBytes(repeatedGot, "input.2.id").String(), gjson.GetBytes(repeatedGot, "input.3.id").String(); id2 != id3 || id2 != "msg_short_repeat_id" {
		t.Fatalf("repeated short invalid IDs = (%q, %q), want msg_short_repeat_id", id2, id3)
	}
}
