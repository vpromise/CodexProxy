package common

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestClaudeFallbackModelRequiresExplicitEvidence(t *testing.T) {
	for _, tc := range []struct{ name, event, want string }{
		{"alias", `{"type":"message_start","message":{"model":"claude-different"}}`, ""},
		{"block", `{"type":"content_block_start","content_block":{"type":"fallback","to":{"model":"claude-opus-5"}}}`, "claude-opus-5"},
		{"unknown_block", `{"type":"content_block_start","content_block":{"type":"text","to":{"model":"claude-opus-5"}}}`, ""},
		{"bad_model", `{"type":"content_block_start","content_block":{"type":"fallback","to":{"model":{"model":"x"}}}}`, ""},
		{"blank_model", `{"type":"content_block_start","content_block":{"type":"fallback","to":{"model":" "}}}`, ""},
		{"wrong_iteration_shape", `{"type":"message_delta","usage":{"iterations":{"type":"fallback_message","model":"claude-opus-5"}}}`, ""},
		{"ordinary_iteration", `{"type":"message_delta","usage":{"iterations":[{"type":"message","model":"claude-opus-5"}]}}`, ""},
		{"last_valid_hop", `{"type":"message_delta","usage":{"iterations":[{"type":"fallback_message","model":"claude-opus-5"},{"type":"fallback_message","model":"claude-opus-4-8"},{"type":"fallback_message","model":null}]}}`, "claude-opus-4-8"},
		{"start_usage", `{"type":"message_start","message":{"usage":{"iterations":[{"type":"fallback_message","model":"claude-opus-5"}]}}}`, "claude-opus-5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeFallbackModel(gjson.Parse(tc.event)); got != tc.want {
				t.Errorf("model = %q, want %q", got, tc.want)
			}
		})
	}
}
