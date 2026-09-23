package helps

import "testing"

func TestClaudeBetaFeaturesProtocolFields(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		want       ClaudeBetaFeatures
	}{
		{"top timing", `{"model":"claude-opus-5-5","output_config":{"timing":{}}}`, ClaudeBetaFeatures{MidConversationTools: true, PerTurnControl: true, Timing: true}},
		{"mythos timing", `{"model":"claude-mythos-5-1","messages":[{"output_config":{"timing":{}}}]}`, ClaudeBetaFeatures{MidConversationTools: true, Timing: true}},
		{"unsupported timing", `{"model":"claude-sonnet-4-6","output_config":{"timing":{}}}`, ClaudeBetaFeatures{}},
		{"inline control", `{"messages":[{"role":"system","clear_at":"next_user_message","content":[{"type":"tool_addition","tool":{"definition":{"name":"Read"}}}]}]}`, ClaudeBetaFeatures{MidConversationTools: true, InlineTools: true, ClearAt: true}},
		{"top cache", `{"cache_control":{"type":"ephemeral","evict_on_complete":true}}`, ClaudeBetaFeatures{MidConversationTools: true, CacheEviction: true}},
		{"system cache", `{"system":[{"type":"text","text":"fixture","cache_control":{"evict_on_complete":true}}]}`, ClaudeBetaFeatures{MidConversationTools: true, CacheEviction: true}},
		{"tool cache", `{"tools":[{"name":"Read","cache_control":{"evict_on_complete":true}}]}`, ClaudeBetaFeatures{MidConversationTools: true, CacheEviction: true}},
		{"message cache", `{"messages":[{"role":"user","content":[{"type":"text","text":"fixture","cache_control":{"evict_on_complete":true}}]}]}`, ClaudeBetaFeatures{MidConversationTools: true, CacheEviction: true}},
		{"legacy inline controls", `{"model":"vendor/CLAUDE-SONNET-4-6","messages":[{"clear_at":"next_user_message","content":[{"type":"tool_addition","tool":{"definition":{}}}]}]}`, ClaudeBetaFeatures{}},
		{"quoted fields", `{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"evict_on_complete clear_at tool_addition output_config.timing"}],"tools":[{"input_schema":{"properties":{"cache_control":{"properties":{"evict_on_complete":{"type":"boolean"}}}}}}]}`, ClaudeBetaFeatures{}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectClaudeBetaFeatures([]byte(tt.body)); got != tt.want {
				t.Fatalf("features=%+v, want %+v", got, tt.want)
			}
		})
	}
}
