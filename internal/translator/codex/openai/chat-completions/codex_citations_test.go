package chat_completions

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCodexChatCitationsNonStream(t *testing.T) {
	raw := []byte(`{"type":"response.completed","response":{"id":"resp_cites","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"前🙂","annotations":[{"type":"url_citation","url":"https://example.com","title":"First","start_index":0,"end_index":2}]},{"type":"output_text","text":"引用","annotations":[{"type":"url_citation","url":"https://example.com","title":"Second","start_index":0,"end_index":2},{"type":"file_citation","file_id":"file_1"},{"type":"url_citation","url":"https://invalid.example","start_index":-1,"end_index":1}]}]}]}}`)
	out := ConvertCodexResponseToOpenAINonStream(t.Context(), "", nil, nil, raw, nil)
	if gjson.GetBytes(out, "choices.0.message.content").String() != "前🙂引用" {
		t.Fatalf("lost content: %s", out)
	}
	annotations := gjson.GetBytes(out, "choices.0.message.annotations").Array()
	if len(annotations) != 2 {
		t.Fatalf("annotations = %s", out)
	}
	for i, a := range annotations {
		if a.Get("type").String() != "url_citation" || a.Get("url").Exists() || a.Get("url_citation.url").String() != "https://example.com" || a.Get("url_citation.start_index").Int() != int64(i*2) || a.Get("url_citation.end_index").Int() != int64(i*2+2) {
			t.Fatalf("invalid annotation %s", a.Raw)
		}
	}
}

func TestCodexChatCitationsStreamOffsetsAndDeduplication(t *testing.T) {
	var param any
	emit := func(s string) [][]byte {
		return ConvertCodexResponseToOpenAI(t.Context(), "gpt-test", nil, nil, []byte("data: "+s), &param)
	}
	check := func(chunks [][]byte, wantStart, wantEnd int64) {
		t.Helper()
		if len(chunks) != 1 {
			t.Fatalf("chunks = %s", chunks)
		}
		a := gjson.GetBytes(chunks[0], "choices.0.delta.annotations.0")
		if a.Get("url_citation.start_index").Int() != wantStart || a.Get("url_citation.end_index").Int() != wantEnd || a.Get("url").Exists() || a.Get("url_citation.url").String() != "https://example.com" {
			t.Fatalf("invalid annotation = %s", a.Raw)
		}
	}
	emit(`{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"前🙂"}`)
	event := `{"type":"response.output_text.annotation.added","output_index":0,"content_index":0,"annotation":{"type":"url_citation","url":"https://example.com","title":"Example","start_index":0,"end_index":2}}`
	check(emit(event), 0, 2)
	if out := emit(event); len(out) != 0 {
		t.Fatalf("duplicate annotation: %s", out)
	}
	emit(`{"type":"response.output_text.delta","output_index":0,"content_index":1,"delta":"引用"}`)
	check(emit(`{"type":"response.content_part.done","output_index":0,"content_index":1,"part":{"type":"output_text","text":"引用","annotations":[{"type":"url_citation","url":"https://example.com","title":"Example","start_index":0,"end_index":2}]}}`), 2, 4)
	if out := emit(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"type":"output_text","text":"前🙂","annotations":[{"type":"url_citation","url":"https://example.com","title":"Example","start_index":0,"end_index":2}]},{"type":"output_text","text":"引用","annotations":[{"type":"url_citation","url":"https://example.com","title":"Example","start_index":0,"end_index":2}]}]}}`); len(out) != 0 {
		t.Fatalf("duplicate completion annotations: %s", out)
	}
	emit(`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"more"}`)
	check(emit(`{"type":"response.output_text.done","output_index":1,"content_index":0,"text":"more","annotations":[{"type":"url_citation","url":"https://example.com","title":"Example","start_index":0,"end_index":4}]}`), 4, 8)
}

func TestCodexChatCitationsRejectInvalidRanges(t *testing.T) {
	for _, span := range []string{`"start_index":-1,"end_index":2`, `"start_index":2,"end_index":1`, `"start_index":"0","end_index":2`, `"start_index":0.5,"end_index":2`, `"end_index":2`, `"start_index":9223372036854775807,"end_index":9223372036854775807`} {
		raw := []byte(`{"type":"response.completed","response":{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"prefix"},{"type":"output_text","text":"text","annotations":[{"type":"url_citation","url":"https://invalid.example",` + span + `}]}]}]}}`)
		if !json.Valid(raw) {
			t.Fatal("bad fixture")
		}
		out := ConvertCodexResponseToOpenAINonStream(t.Context(), "", nil, nil, raw, nil)
		if len(gjson.GetBytes(out, "choices.0.message.annotations").Array()) != 0 {
			t.Fatalf("invalid span forwarded: %s", out)
		}
	}
}

func TestCodexChatCitationsTerminalFallback(t *testing.T) {
	var param any
	emit := func(s string) [][]byte {
		return ConvertCodexResponseToOpenAI(t.Context(), "gpt-test", nil, nil, []byte("data: "+s), &param)
	}
	emit(`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"answer"}`)
	event := `{"type":"response.completed","response":{"output":[{"type":"reasoning"},{"type":"message","content":[{"type":"output_text","text":"answer","annotations":[{"type":"url_citation","url":"https://example.com","start_index":0,"end_index":6}]}]}]}}`
	chunks := emit(event)
	if len(chunks) != 1 || gjson.GetBytes(chunks[0], "choices.0.delta.annotations.0.url_citation.end_index").Int() != 6 || gjson.GetBytes(chunks[0], "choices.0.finish_reason").String() != "stop" {
		t.Fatalf("terminal citation lost: %s", chunks)
	}
	chunks = emit(event)
	if len(chunks) != 1 || gjson.GetBytes(chunks[0], "choices.0.delta.annotations").Exists() {
		t.Fatalf("terminal citation duplicated: %s", chunks)
	}
}
