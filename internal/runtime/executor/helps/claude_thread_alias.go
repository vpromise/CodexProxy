package helps

import (
	"maps"
	"net/http"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
)

// ClaudeThreadAliasStore retains immutable reverse maps within the same caller,
// account, model and egress scope as the corresponding beta snapshot.
// Alias state is deliberately memory-only, matching the upstream repair.
type ClaudeThreadAliasStore struct {
	once    sync.Once
	entries *cache.BoundedLRU[claudeThreadBetaKey, map[string]string]
}

func (s *ClaudeThreadAliasStore) cache() *cache.BoundedLRU[claudeThreadBetaKey, map[string]string] {
	s.once.Do(func() { s.entries = cache.NewBoundedLRU[claudeThreadBetaKey, map[string]string](1024, nil) })
	return s.entries
}

func (s *ClaudeThreadAliasStore) Load(scope, messageID string) (map[string]string, bool) {
	aliases, ok := s.cache().Get(claudeThreadBetaKey{scope: scope, messageID: messageID})
	return maps.Clone(aliases), ok
}

func (s *ClaudeThreadAliasStore) Save(scope, messageID string, aliases map[string]string) {
	if messageID == "" {
		return
	}
	s.cache().GetOrAdd(claudeThreadBetaKey{scope: scope, messageID: messageID}, func() map[string]string { return maps.Clone(aliases) })
}

type ClaudeThreadNotFoundError struct{}

func (ClaudeThreadNotFoundError) Error() string {
	return "No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread."
}
func (ClaudeThreadNotFoundError) StatusCode() int       { return http.StatusNotFound }
func (ClaudeThreadNotFoundError) IsRequestScoped() bool { return true }
func (e ClaudeThreadNotFoundError) ResponseBody() []byte {
	return []byte(`{"type":"error","error":{"type":"not_found_error","message":"` + e.Error() + `"}}`)
}
