package management

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ensurePoolAccountIDsLocked enrolls native records when an administrator opens
// the pool editor. Ordinary proxy startup does not rewrite credential storage.
func (h *Handler) ensurePoolAccountIDsLocked() error {
	next, errLoad := config.LoadConfig(h.configFilePath)
	if errLoad != nil {
		return errLoad
	}
	next.Home = h.cfg.Home
	changed := false
	for i := range next.CodexKey {
		if next.CodexKey[i].PoolAccountID == "" && next.CodexKey[i].APIKey != "" {
			next.CodexKey[i].PoolAccountID = uuid.NewString()
			changed = true
		}
	}
	for i := range next.ClaudeKey {
		if next.ClaudeKey[i].PoolAccountID == "" && next.ClaudeKey[i].APIKey != "" {
			next.ClaudeKey[i].PoolAccountID = uuid.NewString()
			changed = true
		}
	}
	if h.authManager != nil {
		for _, account := range h.authManager.List() {
			if coreauth.AccountSubject(account) == "" || account.Attributes["api_key"] != "" {
				continue
			}
			path := poolAccountPath(next, account)
			if path == "" {
				continue
			}
			data, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			var metadata map[string]any
			if json.Unmarshal(data, &metadata) != nil {
				continue
			}
			if id, _ := metadata["pool_account_id"].(string); id != "" {
				continue
			}
			id := uuid.NewString()
			subject := coreauth.AccountSubject(account)
			if _, errPatch := h.authManager.PatchAuth(context.Background(), account.ID, account.RegistrationEpoch, func(current *coreauth.Auth) error {
				if coreauth.AccountSubject(current) != subject {
					return fmt.Errorf("account identity changed during enrollment")
				}
				if current.Metadata == nil {
					current.Metadata = map[string]any{}
				}
				if currentID, _ := current.Metadata["pool_account_id"].(string); currentID == "" {
					current.Metadata["pool_account_id"] = id
				}
				return nil
			}); errPatch != nil {
				return errPatch
			}
			persisted, errRead := os.ReadFile(path)
			if errRead != nil {
				return errRead
			}
			var saved map[string]any
			if json.Unmarshal(persisted, &saved) != nil {
				return fmt.Errorf("cannot read persisted account identity")
			}
			if currentID, _ := saved["pool_account_id"].(string); currentID == "" {
				return fmt.Errorf("native account store must support durable identity enrollment")
			}

		}
	}
	if changed {
		if err := config.SaveConfigPreserveComments(h.configFilePath, next); err != nil {
			return err
		}
		h.cfg = next
		h.reloadConfigAfterManagementSaveAsync(nil, h.reloadSnapshotConfigLocked())
	}
	h.cfg = next
	return nil
}

func poolAccountPath(cfg *config.Config, account *coreauth.Auth) string {
	if path := account.Attributes["path"]; path != "" {
		return path
	}
	if cfg.AuthDir == "" || account.FileName == "" {
		return ""
	}
	return filepath.Join(cfg.AuthDir, filepath.Base(account.FileName))
}

func (h *Handler) persistedPoolIdentity(account *coreauth.Auth) string {
	if coreauth.AccountSubject(account) == "" {
		return ""
	}
	account = account.Clone()
	key := account.Attributes["api_key"]
	if key != "" {
		configured, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
			Config: h.cfg, IDGenerator: synthesizer.NewStableIDGenerator(),
		})
		if err != nil {
			return ""
		}
		instance := account.Attributes["pool_account_id"]
		var unique *coreauth.Auth
		matches := 0
		for _, current := range configured {
			if current.Provider != account.Provider || current.Attributes["api_key"] != key {
				continue
			}
			// A persisted record ID survives reordering and routing edits. Never
			// remap a removed record to another entry with the same upstream key.
			if instance != "" && current.Attributes["pool_account_id"] == instance {
				return coreauth.AccountIdentity(current)
			}
			if instance == "" && current.ID == account.ID {
				return coreauth.AccountIdentity(current)
			}
			unique, matches = current, matches+1
		}
		if instance == "" && matches == 1 {
			return coreauth.AccountIdentity(unique)
		}
		return ""
	} else if path := poolAccountPath(h.cfg, account); path != "" {
		if data, err := os.ReadFile(path); err == nil {
			var metadata map[string]any
			if json.Unmarshal(data, &metadata) == nil && coreauth.AccountSubject(&coreauth.Auth{Provider: account.Provider, Metadata: metadata}) == coreauth.AccountSubject(account) {
				account.Metadata = metadata
			}
		}
	}
	return coreauth.AccountIdentity(account)
}

// reconcileNativePoolAccounts preserves same-key identities and revokes removed
// native records in the saved policy before asynchronous executor reloads run.
func reconcileNativePoolAccounts(next, previous *config.Config) bool {
	changed := next.PreservePoolAccountIDs(previous)
	before, after := nativeKeyPoolIdentities(previous), nativeKeyPoolIdentities(next)
	for i := range next.AccessIsolation.Pools {
		members := next.AccessIsolation.Pools[i].Accounts
		kept := make([]string, 0, len(members))
		for _, id := range members {
			if before[id] && !after[id] {
				changed = true
				continue
			}
			kept = append(kept, id)
		}
		if len(kept) != len(members) {
			next.AccessIsolation.Pools[i].Accounts = kept
		}
	}
	return changed
}

func nativeKeyPoolIdentities(cfg *config.Config) map[string]bool {
	ids := map[string]bool{}
	if cfg == nil {
		return ids
	}
	add := func(provider, key, instance string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		account := &coreauth.Auth{Provider: provider, Attributes: map[string]string{"api_key": key, "pool_account_id": instance}}
		ids[coreauth.AccountIdentity(account)] = true
	}
	for _, entry := range cfg.CodexKey {
		add("codex", entry.APIKey, entry.PoolAccountID)
	}
	for _, entry := range cfg.ClaudeKey {
		add("claude", entry.APIKey, entry.PoolAccountID)
	}
	return ids
}

// persistNativeKeysLocked commits native credential edits and their pool
// revocations together. Callers must hold h.mu and supply the pre-edit snapshot.
func (h *Handler) persistNativeKeysLocked(c *gin.Context, previous *config.Config) bool {
	reconcileNativePoolAccounts(h.cfg, previous)
	if !h.persistLocked(c) {
		h.cfg = previous
		return false
	}
	return true
}

// prepareUploadedPoolIdentity preserves only a verified same-account lifecycle.
// An imported copy of a deleted record receives a new local record identity.
func prepareUploadedPoolIdentity(path string, data []byte, enroll bool) ([]byte, error) {
	var metadata map[string]any
	if json.Unmarshal(data, &metadata) != nil {
		return data, nil
	}
	provider, _ := metadata["type"].(string)
	incoming := &coreauth.Auth{Provider: provider, Metadata: metadata}
	subject := coreauth.AccountSubject(incoming)
	if subject == "" {
		if _, exists := metadata["pool_account_id"]; !exists {
			return data, nil
		}
		delete(metadata, "pool_account_id")
		return json.Marshal(metadata)
	}
	id := ""
	if previous, err := os.ReadFile(path); err == nil {
		var old map[string]any
		if json.Unmarshal(previous, &old) == nil {
			oldProvider, _ := old["type"].(string)
			if coreauth.AccountSubject(&coreauth.Auth{Provider: oldProvider, Metadata: old}) == subject {
				id, _ = old["pool_account_id"].(string)
			}
		}
	}
	if id == "" {
		if incomingID, _ := metadata["pool_account_id"].(string); incomingID == "" && !enroll {
			return data, nil
		}
		id = uuid.NewString()
	}
	metadata["pool_account_id"] = id
	return json.Marshal(metadata)
}

func (h *Handler) poolIdentityForRemoval(account *coreauth.Auth) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.persistedPoolIdentity(account)
}
