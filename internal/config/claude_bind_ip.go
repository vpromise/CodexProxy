package config

import (
	"fmt"
	"net"
	"strings"
)

// ValidateClaudeBindIPs rejects invalid or unspecified local source addresses.
func (cfg *Config) ValidateClaudeBindIPs() error {
	if cfg == nil {
		return nil
	}
	for index := range cfg.ClaudeKey {
		raw := strings.TrimSpace(cfg.ClaudeKey[index].BindIP)
		if raw == "" {
			continue
		}
		parsed := net.ParseIP(raw)
		if parsed == nil || parsed.IsUnspecified() {
			return fmt.Errorf("claude-api-key[%d].bind-ip is not a valid local source IP", index)
		}
	}
	return nil
}
