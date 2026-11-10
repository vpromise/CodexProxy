package auth

import (
	"crypto/sha256"
	"encoding/hex"
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
	instances := map[string]string{}
	add := func(provider, key, instance string) {
		key = provider + "\x00" + strings.TrimSpace(key)
		if _, exists := instances[key]; exists {
			instances[key] = ""
		} else {
			instances[key] = instance
		}
	}
	for _, entry := range cfg.CodexKey {
		add("codex", entry.APIKey, entry.PoolAccountID)
	}
	for _, entry := range cfg.ClaudeKey {
		add("claude", entry.APIKey, entry.PoolAccountID)
	}
	changed := []*Auth{}
	m.mu.Lock()
	for id, account := range m.auths {
		// Existing IDs belong to the runtime record. Reordering config entries
		// must not retag an old credential before its replacement is synthesized.
		if !IsConfigAPIKeyAuth(account) || IsPluginVirtualAuth(account) || account.Attributes["pool_account_id"] != "" {
			continue
		}
		instance := instances[account.Provider+"\x00"+account.Attributes["api_key"]]
		if instance == "" {
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
