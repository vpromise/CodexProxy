package common

import (
	"fmt"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AttachCacheControl copies a Claude-compatible cache_control object from src onto dst.
// Returns dst unchanged when cache_control is missing or not an object.
func AttachCacheControl(dst []byte, src gjson.Result) []byte {
	cc := src.Get("cache_control")
	if !cc.Exists() || cc.Type == gjson.Null || !cc.IsObject() {
		return dst
	}
	out, err := sjson.SetRawBytes(dst, "cache_control", []byte(cc.Raw))
	if err != nil {
		return dst
	}
	return out
}

// AttachMessageCacheControl applies message-level cache_control onto the last content block.
// Part-level cache_control wins when the last block already has one.
// String content is promoted to a content array so Claude can accept cache_control.
func AttachMessageCacheControl(msg []byte, src gjson.Result) []byte {
	cc := src.Get("cache_control")
	if !cc.Exists() || cc.Type == gjson.Null || !cc.IsObject() {
		return msg
	}

	content := gjson.GetBytes(msg, "content")
	if content.IsArray() {
		arr := content.Array()
		if len(arr) == 0 {
			return msg
		}
		lastIdx := len(arr) - 1
		if arr[lastIdx].Get("cache_control").Exists() {
			return msg
		}
		path := fmt.Sprintf("content.%d.cache_control", lastIdx)
		out, err := sjson.SetRawBytes(msg, path, []byte(cc.Raw))
		if err != nil {
			return msg
		}
		return out
	}

	if content.Type != gjson.String {
		return msg
	}

	textPart := []byte(`{"type":"text","text":""}`)
	textPart, _ = sjson.SetBytes(textPart, "text", content.String())
	textPart, errSet := sjson.SetRawBytes(textPart, "cache_control", []byte(cc.Raw))
	if errSet != nil {
		return msg
	}
	out, err := sjson.SetRawBytes(msg, "content", []byte("[]"))
	if err != nil {
		return msg
	}
	out, _ = sjson.SetRawBytes(out, "content.-1", textPart)
	return out
}

// AttachToolMessageCacheControl hoists the first valid part-level cache_control
// onto the tool_result block, falling back to message-level cache_control.
func AttachToolMessageCacheControl(msg []byte, src gjson.Result) []byte {
	cc := gjson.Result{}
	content := src.Get("content")
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			candidate := part.Get("cache_control")
			if isToolResultCacheControl(candidate) {
				cc = candidate
				return false
			}
			return true
		})
	} else if content.IsObject() {
		cc = content.Get("cache_control")
	}
	if !isToolResultCacheControl(cc) {
		cc = src.Get("cache_control")
	}
	if !isToolResultCacheControl(cc) {
		return msg
	}

	for i, block := range gjson.GetBytes(msg, "content").Array() {
		if block.Get("type").String() != "tool_result" {
			continue
		}
		out, errSet := sjson.SetRawBytes(msg, fmt.Sprintf("content.%d.cache_control", i), []byte(cc.Raw))
		if errSet == nil {
			return out
		}
		return msg
	}
	return msg
}

func isToolResultCacheControl(cc gjson.Result) bool {
	if !cc.IsObject() {
		return false
	}
	typ := cc.Get("type")
	return typ.Type == gjson.String && typ.String() == "ephemeral"
}
