package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// claudeThinkingReplayIsolateSessionKey prevents callers that reuse a session
// identifier from sharing signed thinking content across downstream API keys.
func claudeThinkingReplayIsolateSessionKey(ctx context.Context, sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	if strings.HasPrefix(sessionKey, "execution:") {
		return sessionKey
	}
	apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx))
	if apiKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(apiKey))
	return "caller:" + hex.EncodeToString(sum[:8]) + ":" + sessionKey
}

func shouldClearClaudeThinkingReplayAfterError(err error) bool {
	if err == nil {
		return false
	}
	var upstreamStatus statusErr
	if !errors.As(err, &upstreamStatus) {
		return false
	}
	statusCode := upstreamStatus.StatusCode()
	return statusCode == 400 || statusCode == 422
}

func claudeThinkingReplayContentIsReplayable(content []byte) bool {
	root := gjson.ParseBytes(content)
	if !root.IsArray() {
		return false
	}
	hasSignedThinking := false
	hasToolUse := false
	for _, part := range root.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking":
			if strings.TrimSpace(part.Get("signature").String()) != "" {
				hasSignedThinking = true
			}
		case "tool_use":
			if strings.TrimSpace(part.Get("id").String()) != "" {
				hasToolUse = true
			}
		}
	}
	return hasSignedThinking && hasToolUse
}

func restoreClaudeThinkingReplayContent(body, cachedContent []byte) ([]byte, bool) {
	cachedParts, cachedOK := claudeNonThinkingContentParts(gjson.ParseBytes(cachedContent))
	if !cachedOK {
		return body, false
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body, false
	}
	messageItems := messages.Array()
	for index := len(messageItems) - 1; index >= 0; index-- {
		message := messageItems[index]
		if !strings.EqualFold(strings.TrimSpace(message.Get("role").String()), "assistant") {
			continue
		}
		currentContent := message.Get("content")
		if claudeJSONEqual([]byte(currentContent.Raw), cachedContent) {
			return body, false
		}
		if claudeContentHasThinking(currentContent) {
			continue
		}
		currentParts, currentOK := claudeNonThinkingContentParts(currentContent)
		if !currentOK || !claudeCanonicalPartsEqual(currentParts, cachedParts) {
			continue
		}
		updated, errSet := sjson.SetRawBytes(body, fmt.Sprintf("messages.%d.content", index), cachedContent)
		if errSet != nil {
			return body, false
		}
		return updated, true
	}
	return body, false
}

func claudeContentHasThinking(content gjson.Result) bool {
	if !content.IsArray() {
		return false
	}
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking", "redacted_thinking":
			return true
		}
	}
	return false
}

func claudeNonThinkingContentParts(content gjson.Result) ([][]byte, bool) {
	if !content.IsArray() {
		return nil, false
	}
	parts := make([][]byte, 0, len(content.Array()))
	hasToolUse := false
	for _, part := range content.Array() {
		switch strings.TrimSpace(part.Get("type").String()) {
		case "thinking", "redacted_thinking":
			continue
		case "tool_use":
			if strings.TrimSpace(part.Get("id").String()) == "" {
				return nil, false
			}
			hasToolUse = true
		}
		canonical, ok := claudeCanonicalJSON([]byte(part.Raw))
		if !ok {
			return nil, false
		}
		parts = append(parts, canonical)
	}
	return parts, hasToolUse
}

func claudeCanonicalPartsEqual(left, right [][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if !bytes.Equal(left[i], right[i]) {
			return false
		}
	}
	return true
}

func claudeJSONEqual(left, right []byte) bool {
	canonicalLeft, leftOK := claudeCanonicalJSON(left)
	canonicalRight, rightOK := claudeCanonicalJSON(right)
	return leftOK && rightOK && bytes.Equal(canonicalLeft, canonicalRight)
}

func claudeCanonicalJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if errDecode := decoder.Decode(&value); errDecode != nil {
		return nil, false
	}
	canonical, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, false
	}
	return canonical, true
}

type claudeThinkingReplayStreamBlock struct {
	raw                  []byte
	text                 strings.Builder
	thinking             strings.Builder
	signature            strings.Builder
	input                strings.Builder
	textInitialized      bool
	thinkingInitialized  bool
	signatureInitialized bool
	hasInputDelta        bool
	finished             bool
}

type claudeThinkingReplayStreamAccumulator struct {
	blocks        map[int]*claudeThinkingReplayStreamBlock
	observed      bool
	complete      bool
	upstreamError bool
	abandoned     bool
	bytesUsed     int
}

func newClaudeThinkingReplayStreamAccumulator() *claudeThinkingReplayStreamAccumulator {
	return &claudeThinkingReplayStreamAccumulator{blocks: make(map[int]*claudeThinkingReplayStreamBlock)}
}

func (a *claudeThinkingReplayStreamAccumulator) observe(chunk []byte) {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if !gjson.ValidBytes(payload) {
			a.abandon()
			continue
		}
		root := gjson.ParseBytes(payload)
		switch root.Get("type").String() {
		case "message_start":
			a.observed = true
		case "content_block_start":
			if !a.abandoned {
				a.observeBlockStart(root)
			}
		case "content_block_delta":
			if !a.abandoned {
				a.observeBlockDelta(root)
			}
		case "content_block_stop":
			if !a.abandoned {
				a.finishBlock(int(root.Get("index").Int()))
			}
		case "message_stop":
			a.complete = true
		case "error":
			a.upstreamError = true
			a.abandon()
		}
	}
}

func (a *claudeThinkingReplayStreamAccumulator) observeBlockStart(root gjson.Result) {
	index := int(root.Get("index").Int())
	block := root.Get("content_block")
	if !block.IsObject() || len(a.blocks) >= internalcache.ClaudeThinkingReplayCacheMaxBlocksPerTurn {
		a.abandon()
		return
	}
	if _, exists := a.blocks[index]; exists {
		a.abandon()
		return
	}
	raw := []byte(block.Raw)
	if !a.reserveBytes(len(raw)) {
		return
	}
	a.blocks[index] = &claudeThinkingReplayStreamBlock{raw: append([]byte(nil), raw...)}
}

func (a *claudeThinkingReplayStreamAccumulator) observeBlockDelta(root gjson.Result) {
	index := int(root.Get("index").Int())
	block, ok := a.blocks[index]
	if !ok {
		a.abandon()
		return
	}
	delta := root.Get("delta")
	switch delta.Get("type").String() {
	case "text_delta":
		a.appendBlockText(block, &block.text, &block.textInitialized, "text", delta.Get("text").String())
	case "thinking_delta":
		a.appendBlockText(block, &block.thinking, &block.thinkingInitialized, "thinking", delta.Get("thinking").String())
	case "signature_delta":
		a.appendBlockText(block, &block.signature, &block.signatureInitialized, "signature", delta.Get("signature").String())
	case "input_json_delta":
		suffix := delta.Get("partial_json").String()
		if a.reserveBytes(len(suffix)) {
			block.input.WriteString(suffix)
			block.hasInputDelta = true
		}
	default:
		a.abandon()
	}
}

func (a *claudeThinkingReplayStreamAccumulator) appendBlockText(block *claudeThinkingReplayStreamBlock, builder *strings.Builder, initialized *bool, path, suffix string) {
	if !*initialized {
		initial := gjson.GetBytes(block.raw, path).String()
		if !a.reserveBytes(len(initial)) {
			return
		}
		builder.WriteString(initial)
		*initialized = true
	}
	if a.reserveBytes(len(suffix)) {
		builder.WriteString(suffix)
	}
}

func (a *claudeThinkingReplayStreamAccumulator) finishBlock(index int) {
	block, ok := a.blocks[index]
	if !ok {
		a.abandon()
		return
	}
	if block.hasInputDelta && !gjson.Valid(block.input.String()) {
		a.abandon()
		return
	}
	block.finished = true
}

func (a *claudeThinkingReplayStreamAccumulator) reserveBytes(count int) bool {
	if count < 0 || a.bytesUsed > internalcache.ClaudeThinkingReplayCacheMaxBytesPerSession-count {
		a.abandon()
		return false
	}
	a.bytesUsed += count
	return true
}

func (a *claudeThinkingReplayStreamAccumulator) abandon() {
	a.abandoned = true
	a.blocks = nil
	a.bytesUsed = 0
}

func (a *claudeThinkingReplayStreamAccumulator) content() ([]byte, bool) {
	if !a.observed || !a.complete || a.upstreamError || a.abandoned {
		return nil, false
	}
	indexes := make([]int, 0, len(a.blocks))
	for index := range a.blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	parts := make([][]byte, 0, len(indexes))
	for _, index := range indexes {
		block := a.blocks[index]
		if !block.finished {
			a.abandon()
			return nil, false
		}
		raw := append([]byte(nil), block.raw...)
		var errSet error
		if block.textInitialized {
			raw, errSet = sjson.SetBytes(raw, "text", block.text.String())
		}
		if errSet == nil && block.thinkingInitialized {
			raw, errSet = sjson.SetBytes(raw, "thinking", block.thinking.String())
		}
		if errSet == nil && block.signatureInitialized {
			raw, errSet = sjson.SetBytes(raw, "signature", block.signature.String())
		}
		if errSet == nil && block.hasInputDelta {
			raw, errSet = sjson.SetRawBytes(raw, "input", []byte(block.input.String()))
		}
		if errSet != nil {
			a.abandon()
			return nil, false
		}
		parts = append(parts, raw)
	}
	content := helps.JoinRawJSONArray(parts)
	if len(content) > internalcache.ClaudeThinkingReplayCacheMaxBytesPerSession {
		a.abandon()
		return nil, false
	}
	return content, true
}

type claudeThinkingReplayContentCacheFunc func(context.Context, claudeThinkingReplayScope, []byte)
type claudeThinkingReplayContentClearFunc func(context.Context, claudeThinkingReplayScope)

func wrapClaudeThinkingReplayStreamResult(ctx context.Context, result *cliproxyexecutor.StreamResult, scope claudeThinkingReplayScope, cacheContent claudeThinkingReplayContentCacheFunc, clearContent claudeThinkingReplayContentClearFunc) *cliproxyexecutor.StreamResult {
	if result == nil || !scope.valid() {
		return result
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		accumulator := newClaudeThinkingReplayStreamAccumulator()
		hasError := false
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				hasError = true
			} else {
				accumulator.observe(chunk.Payload)
			}
			select {
			case out <- chunk:
			case <-ctx.Done():
				return
			}
		}
		if hasError {
			return
		}
		if content, completed := accumulator.content(); completed {
			cacheContent(ctx, scope, content)
			return
		}
		if accumulator.upstreamError && scope.replayApplied {
			clearContent(ctx, scope)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: result.Headers.Clone(), Chunks: out}
}
