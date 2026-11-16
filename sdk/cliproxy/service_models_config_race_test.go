package cliproxy

import (
	"context"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"sync"
	"testing"
)

func TestModelRegistrationConcurrentConfigPublication(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	a := &coreauth.Auth{ID: "console-config-publication", Provider: "codex", Metadata: map[string]any{"account_id": "fixture"}}
	defer GlobalModelRegistry().UnregisterClient(a.ID)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			s.cfgMu.Lock()
			s.cfg = &config.Config{}
			s.cfgMu.Unlock()
		}
	}()
	for i := 0; i < 1000; i++ {
		s.registerModelsForAuth(context.Background(), a)
		s.resolveConfigClaudeKey(a)
		s.resolveConfigCodexKey(a)
		s.oauthExcludedModels("codex", "oauth")
	}
	wg.Wait()
}
