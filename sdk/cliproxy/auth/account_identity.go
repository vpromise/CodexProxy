package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// AccountIdentity identifies a verified native principal independently of its
// filename, model aliases, routing options, and renewable access tokens.
// Ambiguous legacy records are deliberately ineligible until re-authenticated.
func AccountIdentity(a *Auth) string {
	subject := AccountSubject(a)
	if subject == "" {
		return ""
	}
	instance := a.Attributes["pool_account_id"]
	if instance == "" {
		instance, _ = a.Metadata["pool_account_id"].(string)
	}
	if instance == "" {
		return subject
	}
	digest := sha256.Sum256([]byte(instance + "\x00" + subject))
	return a.Provider + ":" + hex.EncodeToString(digest[:])
}

// AccountSubject is the verified principal fingerprint used to preserve record
// identity through maintenance. It is never derived from email or filenames.
func AccountSubject(a *Auth) string {
	if a == nil || IsPluginVirtualAuth(a) || (a.Provider != "codex" && a.Provider != "claude") {
		return ""
	}
	if a.Attributes["plugin_id"] != "" || a.Attributes["compat_name"] != "" {
		return ""
	}
	var subject string
	if key := strings.TrimSpace(a.Attributes["api_key"]); key != "" {
		subject = "api-key\x00" + key
	} else if a.Provider == "codex" {
		if id, _ := a.Metadata["account_id"].(string); strings.TrimSpace(id) != "" {
			subject = "oauth\x00" + id
		}
	} else {
		id, _ := a.Metadata["account_uuid"].(string)
		org, _ := a.Metadata["organization_uuid"].(string)
		if strings.TrimSpace(id) != "" && strings.TrimSpace(org) != "" {
			subject = "oauth\x00" + org + "\x00" + id
		}
	}
	if subject == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(a.Provider + "\x00" + subject))
	return a.Provider + ":" + hex.EncodeToString(digest[:])
}

// syncPoolAccountIdentities updates access-only metadata without replacing an
// executor or interrupting its active sessions.
func (m *Manager) syncPoolAccountIdentities(cfg *config.Config) {
	changed := []*Auth{}
	m.mu.Lock()
	for id, account := range m.auths {
		if !IsConfigAPIKeyAuth(account) || IsPluginVirtualAuth(account) {
			continue
		}
		index, err := strconv.Atoi(account.Attributes["config_index"])
		if err != nil || index < 0 {
			continue
		}
		instance := ""
		if account.Provider == "codex" && index < len(cfg.CodexKey) && account.Attributes["api_key"] == cfg.CodexKey[index].APIKey {
			instance = cfg.CodexKey[index].PoolAccountID
		}
		if account.Provider == "claude" && index < len(cfg.ClaudeKey) && account.Attributes["api_key"] == cfg.ClaudeKey[index].APIKey {
			instance = cfg.ClaudeKey[index].PoolAccountID
		}
		if instance == "" || account.Attributes["pool_account_id"] == instance {
			continue
		}
		updated := account.Clone()
		updated.Attributes["pool_account_id"] = instance
		updated.Generation++
		m.auths[id] = updated
		changed = append(changed, updated.Clone())
	}
	m.mu.Unlock()
	if m.scheduler != nil {
		for _, account := range changed {
			m.scheduler.upsertAuth(account)
		}
	}
}
