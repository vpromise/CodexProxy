package helps

import (
	"bytes"

	"github.com/tidwall/gjson"
)

// CodexBootstrapMaxBufferedFrames bounds how many upstream frames may be held back while probing
// for a rejection embedded in an HTTP 200 stream. It counts frames read from the upstream, not
// chunks handed downstream: a frame the downstream translator does not recognise renders as zero
// chunks, so a chunk count is a bound only for the formats that happen to render every frame.
//
// The two transports spend the budget differently: the SSE executor charges one unit per line it
// holds, the websocket executor one per message it reads whether or not it holds it. So the same
// number covers 15 heartbeats under the three-line event:/data:/blank shape a keepalive arrives
// in - 45 lines, with the rejection frame's own event: line spending a 46th - and more under terser
// framings. Counting physical lines keeps the bound independent of the SSE framing style.
const CodexBootstrapMaxBufferedFrames = 48

// CodexBootstrapMaxBufferedBytes caps what a single bootstrap retains, counted over the upstream
// frames and the chunks they translate into. A frame budget alone would not bound that: on SSE
// scanner.Buffer allows 50MB per line, and the websocket dialer sets no read limit at all. The check
// runs before the frame is taken, so one oversized frame cannot be admitted on the strength of an
// empty buffer - but it bounds what is retained, not the peak: the transport has already
// materialised the frame by the time it is consulted.
const CodexBootstrapMaxBufferedBytes = 1 << 20

// IsCodexBootstrapBufferableEvent reports whether a frame may be held back before the downstream
// response headers are committed, i.e. whether nothing observable has happened yet.
//
// The list is closed on purpose. "Nothing has happened yet" cannot be derived from the absence of a
// TTFT token: TTFT deliberately ignores server-side tool traffic such as
// response.shell_call_output_content.delta and its .done counterpart, and holding one of those back
// would let a later rejection replay a tool call, and its side effects, on another credential. An
// unrecognised frame therefore releases the stream.
//
// Beyond the handshake preamble the list covers what upstream interleaves before the first token:
// keepalive heartbeats, and the *.added frames that announce an item or part with no content yet.
func IsCodexBootstrapBufferableEvent(eventType string, payload []byte) bool {
	// An empty data: frame is the SSE heartbeat idiom and carries nothing at all, which is how the
	// websocket loop already treats an empty message. Without this it would fall through to the
	// default and release the stream, turning the feature off for any upstream that sends one.
	if len(bytes.TrimSpace(payload)) == 0 {
		return true
	}
	switch eventType {
	case "response.created", "response.in_progress", "codex.rate_limits", "codex.response.metadata", "keepalive":
		return true
	case "response.output_item.added":
		return isCodexBufferableOutputItem(payload)
	case "response.content_part.added":
		return isCodexEmptyPart(payload)
	case "response.reasoning_summary_part.added":
		return isCodexEmptyPart(payload)
	default:
		return false
	}
}

// isCodexBufferableOutputItem reports whether an announced output item is one the model produces by
// itself and has not started producing, so nothing is running upstream yet. The emptiness checks
// follow the ones IsResponsesTokenEvent applies to response.output_item.done, extended to the
// reasoning summary, which that helper has no case for. Every other item type
// is released, which covers the server-side operations that may already have been dispatched - a
// web_search_call is announced with status "in_progress" and its searching event follows
// immediately, and failing the attempt over after one would run it again on another credential - and
// errs the same way for anything else this list has not been taught about.
func isCodexBufferableOutputItem(payload []byte) bool {
	item := gjson.GetBytes(payload, "item")
	switch item.Get("type").String() {
	case "message":
		return isCodexEmptyContentList(item.Get("content"))
	case "reasoning":
		if item.Get("encrypted_content").String() != "" {
			return false
		}
		return isCodexEmptyContentList(item.Get("summary")) && isCodexEmptyContentList(item.Get("content"))
	case "function_call":
		return item.Get("arguments").String() == ""
	case "custom_tool_call":
		return item.Get("input").String() == ""
	default:
		return false
	}
}

// isCodexEmptyContentList reports whether every entry of an item's content or summary array is a
// textual shape this list knows about and is still empty. An entry whose type is not on the list may
// carry content in a field this check cannot see - an output_audio entry keeps it in "audio" - so it
// is treated as already produced.
func isCodexEmptyContentList(list gjson.Result) bool {
	if list.Exists() && list.Type != gjson.Null && !list.IsArray() {
		return false
	}
	for _, entry := range list.Array() {
		switch entry.Get("type").String() {
		case "output_text", "summary_text", "text", "reasoning_text":
			if entry.Get("text").String() != "" {
				return false
			}
		case "refusal":
			if entry.Get("refusal").String() != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// isCodexEmptyPart reports whether an announced part is a textual one that is still empty. The part
// type is matched against a closed list for the same reason the event type is: a part shape this
// list has not been taught about may carry content in a field the emptiness check cannot see, so it
// releases the stream instead.
func isCodexEmptyPart(payload []byte) bool {
	part := gjson.GetBytes(payload, "part")
	switch part.Get("type").String() {
	case "output_text", "summary_text", "text", "reasoning_text":
		return part.Get("text").String() == ""
	case "refusal":
		return part.Get("refusal").String() == ""
	default:
		return false
	}
}
