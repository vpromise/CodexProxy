package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPoolIdentitySyncDoesNotRetagDuplicateNativeRecords(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, instance := range []string{"", "record-one"} {
			t.Run(provider+"/"+instance, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				account := &Auth{ID: "synthetic-record", Provider: provider, Prefix: "first", Attributes: map[string]string{
					"source": "config:" + provider + "[synthetic]", "api_key": "synthetic-key", "config_index": "0", "pool_account_id": instance,
				}}
				if _, err := manager.Register(context.Background(), account); err != nil {
					t.Fatal(err)
				}
				cfg := &config.Config{}
				if provider == "codex" {
					cfg.CodexKey = []config.CodexKey{
						{APIKey: "synthetic-key", Prefix: "second", PoolAccountID: "record-two"},
						{APIKey: "synthetic-key", Prefix: "first", PoolAccountID: "record-one"},
					}
				} else {
					cfg.ClaudeKey = []config.ClaudeKey{
						{APIKey: "synthetic-key", Prefix: "second", PoolAccountID: "record-two"},
						{APIKey: "synthetic-key", Prefix: "first", PoolAccountID: "record-one"},
					}
				}
				manager.SetConfig(cfg)
				current, ok := manager.GetByID(account.ID)
				if !ok || AccountIdentity(current) != AccountIdentity(account) {
					t.Fatal("config reordering retagged the old runtime record before credential synthesis")
				}
			})
		}
	}
}

func TestPoolIdentitySyncEnrollsUniqueNativeKey(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			account := &Auth{ID: "synthetic-record", Provider: provider, Attributes: map[string]string{
				"source": "config:" + provider + "[synthetic]", "api_key": "synthetic-key", "config_index": "0",
			}}
			if _, err := manager.Register(context.Background(), account); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{}
			if provider == "codex" {
				cfg.CodexKey = []config.CodexKey{{APIKey: "synthetic-key", PoolAccountID: "record-one"}}
			} else {
				cfg.ClaudeKey = []config.ClaudeKey{{APIKey: "synthetic-key", PoolAccountID: "record-one"}}
			}
			manager.SetConfig(cfg)
			current, ok := manager.GetByID(account.ID)
			if !ok || current.Attributes["pool_account_id"] != "record-one" {
				t.Fatal("unique native key was not enrolled")
			}
		})
	}
}
