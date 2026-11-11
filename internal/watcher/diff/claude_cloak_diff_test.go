package diff

import (
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestClaudeCloakChangeDetails(t *testing.T) {
	enabled, disabled := true, false
	for _, tc := range []struct {
		name     string
		old, new *config.CloakConfig
		want     []string
	}{
		{"added", nil, &config.CloakConfig{}, []string{"claude[0].cloak: added"}},
		{"removed", &config.CloakConfig{}, nil, []string{"claude[0].cloak: removed"}},
		{"equal_word_count", &config.CloakConfig{SensitiveWords: []string{"old-secret"}}, &config.CloakConfig{SensitiveWords: []string{"new-secret"}}, []string{"claude[0].cloak.sensitive-words: updated (1 -> 1 entries)"}},
		{"cache_added", &config.CloakConfig{}, &config.CloakConfig{CacheUserID: &disabled}, []string{"claude[0].cloak.cache-user-id: inherit -> false"}},
		{"cache_changed", &config.CloakConfig{CacheUserID: &enabled}, &config.CloakConfig{CacheUserID: &disabled}, []string{"claude[0].cloak.cache-user-id: true -> false"}},
		{"unchanged", &config.CloakConfig{SensitiveWords: []string{"secret"}}, &config.CloakConfig{SensitiveWords: []string{"secret"}}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldCfg := &config.Config{ClaudeKey: []config.ClaudeKey{{Cloak: tc.old}}}
			newCfg := &config.Config{ClaudeKey: []config.ClaudeKey{{Cloak: tc.new}}}
			got := BuildConfigChangeDetails(oldCfg, newCfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("changes = %v, want %v", got, tc.want)
			}
			if strings.Contains(strings.Join(got, "\n"), "secret") {
				t.Errorf("sensitive words exposed in change details: %v", got)
			}
		})
	}
}
