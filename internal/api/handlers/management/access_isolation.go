package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type poolAccount struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	Disabled      bool   `json:"disabled"`
	APIKeyPreview string `json:"api_key_preview,omitempty"`
	AuthIndex     string `json:"auth_index,omitempty"`
	Prefix        string `json:"prefix,omitempty"`
}

func (h *Handler) poolAccounts() []poolAccount {
	accounts := []poolAccount{}
	seen := map[string]bool{}
	if h.authManager == nil {
		return accounts
	}
	for _, a := range h.authManager.List() {
		id := h.persistedPoolIdentity(a)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		label := a.Label
		if label == "" {
			label = a.ID
		}
		accounts = append(accounts, poolAccount{
			ID: id, Provider: a.Provider, Label: label, Disabled: a.Disabled,
			APIKeyPreview: poolAPIKeyPreview(a.Attributes["api_key"]),
			AuthIndex:     a.EnsureIndex(),
			Prefix:        a.Prefix,
		})
	}
	sort.Slice(accounts, func(i, j int) bool { return accounts[i].ID < accounts[j].ID })
	return accounts
}

func poolAPIKeyPreview(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	chars := []rune(key)
	// Match the provider page preview while hiding every character of short keys.
	if len(chars) <= 4 {
		return "**********"
	}
	return string(chars[:2]) + "******" + string(chars[len(chars)-2:])
}

func (h *Handler) accessRevision() (string, error) {
	data, err := os.ReadFile(h.configFilePath)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (h *Handler) GetAccessIsolation(c *gin.Context) {
	h.authStatusMu.Lock()
	defer h.authStatusMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.ensurePoolAccountIDsLocked(); err != nil {
		c.JSON(500, gin.H{"error": "cannot persist account identities"})
		return
	}
	revision, err := h.accessRevision()
	if err != nil {
		c.JSON(500, gin.H{"error": "cannot read configuration"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"revision": revision, "settings": h.cfg.AccessIsolation, "accounts": h.poolAccounts(), "api-keys": h.cfg.APIKeys})
}

func (h *Handler) validatePoolAccounts(next *config.Config) error {
	if err := next.ValidateAccessIsolation(); err != nil {
		return err
	}
	known := map[string]string{}
	for _, a := range h.poolAccounts() {
		known[a.ID] = a.Provider
	}
	// Retain temporarily unavailable existing members; reject new unknown grants.
	existing := map[string]bool{}
	for _, p := range h.cfg.AccessIsolation.Pools {
		for _, id := range p.Accounts {
			existing[p.ID+"\x00"+id] = true
		}
	}
	for _, pool := range next.AccessIsolation.Pools {
		for _, id := range pool.Accounts {
			if known[id] != pool.Provider && !existing[pool.ID+"\x00"+id] {
				return fmt.Errorf("pool %q contains an unknown or foreign account", pool.ID)
			}
		}
	}
	return nil
}

func (h *Handler) PutAccessIsolation(c *gin.Context) {
	var body struct {
		Revision string                 `json:"revision"`
		Settings config.AccessIsolation `json:"settings"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(400, gin.H{"error": "invalid body"})
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	revision, err := h.accessRevision()
	if err != nil {
		c.JSON(500, gin.H{"error": "cannot read configuration"})
		return
	}
	if body.Revision == "" || body.Revision != revision {
		c.JSON(409, gin.H{"error": "configuration changed; reload before saving"})
		return
	}
	next, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		c.JSON(422, gin.H{"error": "invalid configuration"})
		return
	}
	next.Home = h.cfg.Home
	next.AccessIsolation = body.Settings
	if err := h.validatePoolAccounts(next); err != nil {
		c.JSON(422, gin.H{"error": err.Error()})
		return
	}
	previous := h.cfg
	h.cfg = next
	if !h.persistLocked(c) {
		h.cfg = previous
	}
}

// GetManagementModels uses management authorization, independently of client keys.
func (h *Handler) GetManagementModels(c *gin.Context) {
	models := []map[string]any{}
	if h.authManager != nil {
		for _, model := range h.authManager.AuthorizedModels(nil) {
			models = append(models, registry.GetGlobalRegistry().FormatModel(model, "openai"))
		}
	}
	c.JSON(200, gin.H{"object": "list", "data": models})
}

// RevokeAccountPoolMember ends membership before a credential can be reimported.
func (h *Handler) RevokeAccountPoolMember(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	h.mu.Lock()
	if h.cfg == nil || len(h.cfg.AccessIsolation.Pools) == 0 {
		h.mu.Unlock()
		return nil
	}
	next, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	next.Home = h.cfg.Home
	changed := false
	for i := range next.AccessIsolation.Pools {
		members := next.AccessIsolation.Pools[i].Accounts
		kept := make([]string, 0, len(members))
		for _, member := range members {
			if member == id {
				changed = true
			} else {
				kept = append(kept, member)
			}
		}
		next.AccessIsolation.Pools[i].Accounts = kept
	}
	if !changed {
		h.mu.Unlock()
		return nil
	}
	if errSave := config.SaveConfigPreserveComments(h.configFilePath, next); errSave != nil {
		h.mu.Unlock()
		return errSave
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	// Publish the reduced grant before removing the account. Existing requests hold
	// independent scopes; new requests cannot regain access through an import race.
	if h.authManager != nil {
		h.authManager.SetConfig(next)
	}
	h.mu.Unlock()
	h.reloadConfigAfterManagementSaveAsync(ctx, snapshot)
	return nil
}
