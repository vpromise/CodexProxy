package common

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// UnsupportedPartError reports a user turn emptied by an unrepresentable part.
type UnsupportedPartError struct{ Type string }

func (e *UnsupportedPartError) Error() string {
	return fmt.Sprintf("unsupported content part: %s (user turn has no representable content)", e.Type)
}
func (*UnsupportedPartError) StatusCode() int       { return 400 }
func (*UnsupportedPartError) IsRequestScoped() bool { return true }

// UserTurnDrops tracks losses per source user turn. Other turns and instructions
// cannot hide an emptied turn; representable content in the same turn can.
// A nil tracker keeps legacy byte-only converters unchanged.
type UserTurnDrops struct{ turn, first string }

func (d *UserTurnDrops) Drop(partType string) {
	if d != nil && d.turn == "" {
		d.turn = partType
	}
}
func (d *UserTurnDrops) EndTurn(sendable int) {
	if d == nil {
		return
	}
	if d.turn != "" && sendable == 0 && d.first == "" {
		d.first = d.turn
	}
	d.turn = ""
}
func (d *UserTurnDrops) Err() error {
	if d == nil || d.first == "" {
		return nil
	}
	return &UnsupportedPartError{Type: d.first}
}

// IsAttachmentPart identifies media whose loss must not erase a user turn.
func IsAttachmentPart(kind string) bool {
	switch kind {
	case "file", "input_file", "input_audio", "image", "image_url", "input_image", "document", "container_upload":
		return true
	}
	return false
}

// CountSendableParts excludes blank text without rewriting the outgoing body.
func CountSendableParts(parts [][]byte) int {
	count := 0
	for _, part := range parts {
		value := gjson.ParseBytes(part)
		switch value.Get("type").String() {
		case "text", "input_text", "output_text":
			if strings.TrimSpace(value.Get("text").String()) != "" {
				count++
			}
		default:
			count++
		}
	}
	return count
}
