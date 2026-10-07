package chat_completions

import (
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type citationPartKey struct{ output, content int64 }

// Chat annotations use a nested url_citation object and character offsets in
// the concatenated message, rather than offsets in an individual Responses part.
func chatURLCitation(annotation gjson.Result, offset int64) []byte {
	if annotation.Get("type").String() != "url_citation" || annotation.Get("url").String() == "" {
		return nil
	}
	startValue, endValue := annotation.Get("start_index"), annotation.Get("end_index")
	if startValue.Type != gjson.Number || endValue.Type != gjson.Number {
		return nil
	}
	start, errStart := strconv.ParseInt(startValue.Raw, 10, 64)
	end, errEnd := strconv.ParseInt(endValue.Raw, 10, 64)
	if errStart != nil || errEnd != nil || offset < 0 || start < 0 || end < start || end > math.MaxInt64-offset {
		return nil
	}
	out := []byte(`{"type":"url_citation","url_citation":{}}`)
	out, _ = sjson.SetBytes(out, "url_citation.url", annotation.Get("url").String())
	out, _ = sjson.SetBytes(out, "url_citation.title", annotation.Get("title").String())
	out, _ = sjson.SetBytes(out, "url_citation.start_index", offset+start)
	out, _ = sjson.SetBytes(out, "url_citation.end_index", offset+end)
	return out
}

func (p *ConvertCliToOpenAIParams) noteTextPart(event gjson.Result, text string) {
	if p.textPartOffsets == nil {
		p.textPartOffsets = make(map[citationPartKey]int64)
	}
	key := citationPartKey{event.Get("output_index").Int(), event.Get("content_index").Int()}
	if _, exists := p.textPartOffsets[key]; !exists {
		p.textPartOffsets[key] = p.emittedTextRunes
	}
	p.emittedTextRunes += int64(utf8.RuneCountInString(text))
}

func (p *ConvertCliToOpenAIParams) streamCitations(event gjson.Result) [][]byte {
	var out [][]byte
	collect := func(output, content int64, annotations []gjson.Result) {
		offset, exists := p.textPartOffsets[citationPartKey{output, content}]
		if !exists {
			return
		}
		for _, annotation := range annotations {
			converted := chatURLCitation(annotation, offset)
			if len(converted) == 0 {
				continue
			}
			if p.seenCitations == nil {
				p.seenCitations = make(map[string]struct{})
			}
			key := string(converted)
			if _, exists := p.seenCitations[key]; exists {
				continue
			}
			p.seenCitations[key] = struct{}{}
			out = append(out, converted)
		}
	}
	collectItem := func(output int64, item gjson.Result) {
		if item.Get("type").String() != "message" {
			return
		}
		for i, part := range item.Get("content").Array() {
			if part.Get("type").String() == "output_text" {
				collect(output, int64(i), part.Get("annotations").Array())
			}
		}
	}
	output, content := event.Get("output_index").Int(), event.Get("content_index").Int()
	switch event.Get("type").String() {
	case "response.output_text.annotation.added":
		collect(output, content, []gjson.Result{event.Get("annotation")})
	case "response.output_text.done":
		collect(output, content, event.Get("annotations").Array())
	case "response.content_part.done":
		if event.Get("part.type").String() == "output_text" {
			collect(output, content, event.Get("part.annotations").Array())
		}
	case "response.output_item.done":
		collectItem(output, event.Get("item"))
	case "response.completed", "response.incomplete":
		for i, item := range event.Get("response.output").Array() {
			collectItem(int64(i), item)
		}
	}
	return out
}
