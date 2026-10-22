package config

import (
	"fmt"
	"strings"
)

// AccessIsolation binds client keys to explicit, single-provider account pools.
type AccessIsolation struct {
	Enabled bool               `yaml:"enabled" json:"enabled"`
	Pools   []AccountPool      `yaml:"pools" json:"pools"`
	Keys    []ClientKeyBinding `yaml:"keys" json:"keys"`
}

type AccountPool struct {
	ID       string   `yaml:"id" json:"id"`
	Name     string   `yaml:"name" json:"name"`
	Provider string   `yaml:"provider" json:"provider"`
	Accounts []string `yaml:"accounts" json:"accounts"`
}

type ClientKeyBinding struct {
	Key  string `yaml:"key" json:"key"`
	Name string `yaml:"name,omitempty" json:"name"`
	Note string `yaml:"note,omitempty" json:"note"`
	Pool string `yaml:"pool,omitempty" json:"pool"`
}

// ValidateAccessIsolation validates policy independently of account availability.
// Missing accounts fail closed at selection time; an outage must not invalidate config.
func (c *Config) ValidateAccessIsolation() error {
	if c.AccessIsolation.Enabled && c.Home.Enabled {
		return fmt.Errorf("access isolation cannot be enabled with Home")
	}
	pools := make(map[string]bool)
	for _, pool := range c.AccessIsolation.Pools {
		if strings.TrimSpace(pool.ID) == "" || strings.TrimSpace(pool.Name) == "" || pools[pool.ID] {
			return fmt.Errorf("account pools require unique IDs and nonempty names")
		}
		if pool.Provider != "codex" && pool.Provider != "claude" {
			return fmt.Errorf("account pool provider must be codex or claude")
		}
		pools[pool.ID] = true
		members := make(map[string]bool)
		for _, id := range pool.Accounts {
			if !strings.HasPrefix(id, pool.Provider+":") || members[id] {
				return fmt.Errorf("invalid or duplicate account identity in pool %q", pool.ID)
			}
			members[id] = true
		}
	}
	keys := make(map[string]bool)
	for _, key := range c.AccessIsolation.Keys {
		if strings.TrimSpace(key.Key) == "" || keys[key.Key] {
			return fmt.Errorf("client key bindings require unique nonempty keys")
		}
		keys[key.Key] = true
		if key.Pool != "" && !pools[key.Pool] {
			return fmt.Errorf("client key refers to missing account pool %q", key.Pool)
		}
	}
	return nil
}

// PreservePoolAccountIDs keeps record identity through a verified key edit.
// Ambiguous duplicate keys require an explicit existing record identity.
func (c *Config) PreservePoolAccountIDs(previous *Config) bool {
	changed := false
	if previous == nil {
		return false
	}
	for i := range c.CodexKey {
		if c.CodexKey[i].PoolAccountID != "" {
			continue
		}
		matches, id := 0, ""
		for _, old := range previous.CodexKey {
			if old.APIKey == c.CodexKey[i].APIKey {
				matches++
				id = old.PoolAccountID
			}
		}
		if matches == 1 && id != "" {
			changed = true
			c.CodexKey[i].PoolAccountID = id
		}
	}
	for i := range c.ClaudeKey {
		if c.ClaudeKey[i].PoolAccountID != "" {
			continue
		}
		matches, id := 0, ""
		for _, old := range previous.ClaudeKey {
			if old.APIKey == c.ClaudeKey[i].APIKey {
				matches++
				id = old.PoolAccountID
			}
		}
		if matches == 1 && id != "" {
			changed = true
			c.ClaudeKey[i].PoolAccountID = id
		}
	}
	return changed
}
