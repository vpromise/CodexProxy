package claudereplay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const redactedPrefix = "claude-redacted-thinking:"

// MaxContentBytes bounds the incremental response collector before encryption.
const MaxContentBytes = maxRecordSize - 4096

type responseTurnKey struct{}

// Turn belongs to one upstream attempt. It becomes readable after completion.
type Turn struct {
	store    *Store
	scope    Scope
	id       string
	prefix   string
	messages int
	restored bool
}

type claim struct {
	index   int
	carrier string
	active  bool
}

type references struct {
	claims map[string][]claim
	calls  []string
}

func (r references) hasActiveClaim() bool {
	for _, group := range r.claims {
		for _, item := range group {
			if item.active {
				return true
			}
		}
	}
	return false
}

// Prepare builds a replay plan after request translation and before dispatch.
func (s *Store) Prepare(ctx context.Context, scope Scope, original, body []byte) ([]byte, *Turn, error) {
	if !s.enabled(scope) {
		return body, nil, nil
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() || gjson.GetBytes(body, "thread").Exists() {
		return body, nil, nil
	}
	refs, err := s.readReferences(scope, gjson.GetBytes(original, "input"))
	if err != nil {
		return nil, nil, err
	}
	records, err := s.readRecords(ctx, scope, refs)
	if err != nil {
		return nil, nil, err
	}
	parts := messages.Array()
	prepared, digest, err := s.restore(body, parts, scope, refs, records)
	if err != nil {
		return nil, nil, err
	}
	return prepared, &Turn{store: s, scope: scope, id: newID(), prefix: digest, messages: len(parts), restored: len(records) != 0 && !bytes.Equal(body, prepared)}, nil
}

func (s *Store) readReferences(scope Scope, input gjson.Result) (references, error) {
	refs := references{claims: make(map[string][]claim)}
	items := input.Array()
	lastOutput := len(items)
	for lastOutput > 0 {
		item := items[lastOutput-1]
		switch item.Get("type").String() {
		case "function_call_output", "custom_tool_call_output":
			if call := item.Get("call_id").String(); call != "" && !slices.Contains(refs.calls, call) {
				refs.calls = append(refs.calls, call)
			}
			if len(refs.calls) > maxLookup {
				return references{}, UnavailableError{}
			}
			lastOutput--
			continue
		}
		break
	}
	activeFrom := lastOutput
	if len(refs.calls) != 0 {
		for activeFrom > 0 {
			item := items[activeFrom-1]
			switch item.Get("type").String() {
			case "reasoning", "function_call", "custom_tool_call", "web_search_call":
				activeFrom--
				continue
			case "message":
				if item.Get("role").String() == "assistant" {
					activeFrom--
					continue
				}
			}
			break
		}
	}
	for pos, item := range items {
		if item.Get("type").String() != "reasoning" {
			continue
		}
		ref := item.Get("id").String()
		if !strings.HasPrefix(ref, refPrefix) {
			continue
		}
		id, index, valid := s.parseRef(scope, ref)
		if !valid {
			return references{}, UnavailableError{}
		}
		group := refs.claims[id]
		if (len(refs.claims) >= maxLookup && group == nil) || len(group) >= maxBlocks {
			return references{}, UnavailableError{}
		}
		refs.claims[id] = append(group, claim{index: index, carrier: item.Get("encrypted_content").String(), active: pos >= activeFrom && pos < lastOutput})
	}
	return refs, nil
}

func (s *Store) readRecords(ctx context.Context, scope Scope, refs references) ([]record, error) {
	if len(refs.claims) == 0 && len(refs.calls) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(refs.claims))
	for id := range refs.claims {
		ids = append(ids, id)
	}
	var calls []string
	if !refs.hasActiveClaim() {
		calls = refs.calls
	}
	records, err := s.find(ctx, scope, ids, calls)
	if err == nil {
		return records, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	log.Warn("Claude replay lookup failed")
	if refs.hasActiveClaim() {
		return nil, UnavailableError{}
	}
	return nil, nil
}

func (s *Store) restore(body []byte, messages []gjson.Result, scope Scope, refs references, records []record) ([]byte, string, error) {
	binding := s.binding(scope)
	byPosition := make(map[int][]record)
	required := make(map[string]bool)
	for id, claims := range refs.claims {
		for _, item := range claims {
			if item.active {
				required[id] = true
			}
		}
	}
	for _, value := range records {
		claims := refs.claims[value.ID]
		if value.Binding != binding || !verifyClaims(value.Content, claims) {
			continue
		}
		if required[value.ID] && !matchesToolCalls(gjson.ParseBytes(value.Content).Array(), refs.calls) {
			continue
		}
		byPosition[value.MessageCount] = append(byPosition[value.MessageCount], value)
	}

	prefix := initialPrefix(body)
	changed := false
	for position, msg := range messages {
		// Cache annotations can move to a later turn. Canonicalize the single
		// text shorthand on the wire so the earlier message stays equivalent.
		if content := msg.Get("content"); content.Type == gjson.String {
			text := `[{"type":"text","text":` + content.Raw + `}]`
			raw, err := sjson.SetRaw(msg.Raw, "content", text)
			if err != nil {
				return nil, "", UnavailableError{}
			}
			msg = gjson.Parse(raw)
			changed = true
		}
		var match record
		var restored []byte
		for _, candidate := range byPosition[position] {
			if prefix != candidate.Prefix {
				continue
			}
			if matchesToolCalls(gjson.ParseBytes(candidate.Content).Array(), refs.calls) {
				required[candidate.ID] = true
			}
			content, ok := restoreContent(msg, candidate.Content)
			if !ok {
				continue
			}
			if restored != nil {
				return nil, "", UnavailableError{}
			}
			match, restored = candidate, content
		}
		if restored != nil {
			raw, err := sjson.SetRaw(msg.Raw, "content", string(restored))
			if err != nil {
				return nil, "", UnavailableError{}
			}
			msg = gjson.Parse(raw)
			changed = true
			delete(required, match.ID)
		}
		messages[position] = msg
		prefix = advancePrefix(prefix, msg)
	}
	if len(required) != 0 {
		return nil, "", UnavailableError{}
	}
	if changed {
		parts := make([]json.RawMessage, len(messages))
		for i, msg := range messages {
			parts[i] = json.RawMessage(msg.Raw)
		}
		raw, err := json.Marshal(parts)
		if err != nil {
			return nil, "", UnavailableError{}
		}
		body, err = sjson.SetRawBytes(body, "messages", raw)
		if err != nil {
			return nil, "", UnavailableError{}
		}
	}
	return body, prefix, nil
}

func verifyClaims(content []byte, claims []claim) bool {
	parts := gjson.ParseBytes(content).Array()
	prev := -1
	for _, item := range claims {
		if item.index < prev || item.index >= len(parts) {
			return false
		}
		if carrier := reasoningCarrier(parts[item.index]); carrier == "" || (item.carrier != "" && item.carrier != carrier) {
			return false
		}
		prev = item.index
	}
	return true
}

func reasoningCarrier(block gjson.Result) string {
	switch block.Get("type").String() {
	case "thinking":
		return block.Get("signature").String()
	case "redacted_thinking":
		if data := block.Get("data").String(); data != "" {
			return redactedPrefix + data
		}
	}
	return ""
}

func isThinking(block gjson.Result) bool {
	switch block.Get("type").String() {
	case "thinking", "redacted_thinking":
		return true
	default:
		return false
	}
}

func matchesToolCalls(blocks []gjson.Result, calls []string) bool {
	if len(calls) == 0 {
		return false
	}
	matched := make(map[string]bool, len(calls))
	for _, block := range blocks {
		if block.Get("type").String() == "tool_use" && slices.Contains(calls, block.Get("id").String()) {
			matched[block.Get("id").String()] = true
		}
	}
	return len(matched) == len(calls)
}

// Keep the current visible blocks and their cache annotations after comparing
// their contents with the recorded assistant response.
func restoreContent(msg gjson.Result, original []byte) ([]byte, bool) {
	if msg.Get("role").String() != "assistant" {
		return nil, false
	}
	content := msg.Get("content")
	var current []gjson.Result
	if content.Type == gjson.String {
		text, _ := json.Marshal(map[string]string{"type": "text", "text": content.String()})
		current = []gjson.Result{gjson.ParseBytes(text)}
	} else if content.IsArray() {
		current = content.Array()
	} else {
		return nil, false
	}

	parts := gjson.ParseBytes(original).Array()
	out := make([]json.RawMessage, 0, len(parts))
	for _, part := range parts {
		matches := len(current) > 0 && comparableBlock(part) == comparableBlock(current[0])
		if isThinking(part) {
			// A missing signed block can be recovered in place. Advancing only on
			// a match also keeps existing signed blocks inside their tool boundaries.
			out = append(out, json.RawMessage(part.Raw))
			if matches {
				current = current[1:]
			}
			continue
		}
		if !matches {
			return nil, false
		}
		out = append(out, json.RawMessage(current[0].Raw))
		current = current[1:]
	}
	if len(current) != 0 {
		return nil, false
	}
	raw, err := json.Marshal(out)
	return raw, err == nil
}

func comparableBlock(part gjson.Result) string {
	if part.Get("type").String() != "thinking" {
		return canonicalPart(part)
	}
	if normalized, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, part.Get("signature").String()); ok {
		if raw, err := sjson.Set(part.Raw, "signature", normalized); err == nil {
			return canonicalPart(gjson.Parse(raw))
		}
	}
	return canonicalPart(part)
}

// RequestLogBody keeps restored stored data out of the existing raw request log.
func (t *Turn) RequestLogBody(body []byte) []byte {
	if t != nil && t.restored {
		return nil
	}
	return body
}

func canonicalPart(part gjson.Result) string {
	if !part.Exists() {
		return "null"
	}
	dec := json.NewDecoder(strings.NewReader(part.Raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return part.Raw
	}
	removeCacheControl(value)
	out, err := json.Marshal(value)
	if err != nil {
		return part.Raw
	}
	return string(out)
}

// Cache annotations are outside Anthropic's prefix comparison. Do not modify
// similarly named properties inside tool input or other arbitrary JSON.
func removeCacheControl(value any) {
	switch typed := value.(type) {
	case []any:
		for _, part := range typed {
			removeCacheControl(part)
		}
	case map[string]any:
		delete(typed, "cache_control")
		if content, ok := typed["content"].([]any); ok {
			removeCacheControl(content)
		}
	}
}

func digest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		hash.Write([]byte(strconv.Itoa(len(part))))
		hash.Write([]byte{':'})
		hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func initialPrefix(body []byte) string {
	parsed := gjson.ParseBytes(body)
	return digest(canonicalPart(parsed.Get("system")), canonicalPart(parsed.Get("tools")))
}

func advancePrefix(prefix string, msg gjson.Result) string {
	return digest(prefix, canonicalPart(msg))
}

// ResponseContext supplies consistent IDs to all events for the same output block.
func (t *Turn) ResponseContext(ctx context.Context) context.Context {
	if t == nil {
		return ctx
	}
	return context.WithValue(ctx, responseTurnKey{}, t)
}

// ReasoningItemID returns the replay reference when a turn opted into it.
func ReasoningItemID(ctx context.Context, legacy string, index int) string {
	if ctx != nil {
		if turn, ok := ctx.Value(responseTurnKey{}).(*Turn); ok {
			if ref := turn.store.ref(turn.scope, turn.id, index); ref != "" {
				return ref
			}
		}
	}
	return legacy
}

// Complete saves only the exact finalized assistant turn with signed content.
func (t *Turn) Complete(ctx context.Context, content []byte) {
	if t == nil || ctx.Err() != nil || len(content) > MaxContentBytes {
		return
	}
	content = bytes.TrimSpace(content)
	parsed := gjson.ParseBytes(content)
	if !parsed.IsArray() {
		return
	}
	parts := parsed.Array()
	if len(parts) == 0 || len(parts) > maxBlocks {
		return
	}
	var calls []string
	signed := false
	for _, part := range parts {
		if isThinking(part) {
			if reasoningCarrier(part) == "" {
				return
			}
			if part.Get("type").String() == "thinking" {
				if _, ok := sigcompat.CompatibleSignatureForProvider(sigcompat.SignatureProviderClaude, reasoningCarrier(part)); !ok {
					return
				}
			}
			signed = true
		}
		if part.Get("type").String() == "tool_use" {
			if call := part.Get("id").String(); call != "" {
				calls = append(calls, call)
			}
		}
	}
	if !signed {
		return
	}
	value := record{
		ID: base64.RawURLEncoding.EncodeToString([]byte(t.id)), Binding: t.store.binding(t.scope),
		Prefix: t.prefix, MessageCount: t.messages, Content: json.RawMessage(content),
	}
	if err := t.store.save(ctx, t.scope, t.id, value, calls); err != nil && ctx.Err() == nil {
		log.Warn("Claude replay record write failed")
	}
}
