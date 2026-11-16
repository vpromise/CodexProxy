package management

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"os"
	"path/filepath"
)

func (n *consoleNode) policyRevision() string {
	n.h.mu.Lock()
	defer n.h.mu.Unlock()
	revision, _ := n.h.accessRevision()
	return revision
}

// channelKey issues an inference-only key for one existing pool. Provider
// credentials and general management permissions never cross this Interface.
func (n *consoleNode) channelKey(c *gin.Context) {
	var body struct {
		OperationID      string `json:"operation_id"`
		PoolID           string `json:"pool_id"`
		ExpectedRevision string `json:"expected_revision"`
	}
	if c.ShouldBindJSON(&body) != nil || !selfServiceID(body.OperationID) || body.PoolID == "" {
		c.Status(422)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	name := "console-channel/" + body.OperationID
	path := filepath.Join(n.dir, body.OperationID+".json")
	fresh := false
	var op *consoleOperation
	if raw, err := os.ReadFile(path); err == nil {
		var existing consoleOperation
		if json.Unmarshal(raw, &existing) != nil || existing.Kind != "channel_key" || existing.Digest != digestJSON(body) {
			c.Status(409)
			return
		}
		op = &existing
	} else {
		var ok bool
		op, ok = n.begin(c, "channel_key", body.OperationID, "", body)
		fresh = ok
		if !ok {
			return
		}
	}
	h := n.h
	h.mu.Lock()
	defer h.mu.Unlock()
	next, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		n.finish(c, op, err, nil)
		return
	}
	if !next.AccessIsolation.Enabled || next.Home.Enabled {
		n.finish(c, op, errors.New("isolation required"), nil)
		return
	}
	found := false
	for _, pool := range next.AccessIsolation.Pools {
		if pool.ID == body.PoolID {
			found = true
		}
	}
	if !found {
		n.finish(c, op, errors.New("pool unavailable"), nil)
		return
	}
	for _, binding := range next.AccessIsolation.Keys {
		if binding.Name == name {
			present := false
			for _, key := range next.APIKeys {
				present = present || key == binding.Key
			}
			if binding.Pool != body.PoolID || !present {
				n.finish(c, op, errors.New("key changed"), nil)
				return
			}
			n.finish(c, op, nil, gin.H{"key": binding.Key, "pool_id": binding.Pool})
			return
		}
	}
	// An old completed receipt must never recreate an administrator-deleted key.
	if !fresh {
		c.JSON(409, gin.H{"error": "key_removed_or_changed"})
		return
	}
	revision, err := h.accessRevision()
	if err != nil || body.ExpectedRevision == "" || revision != body.ExpectedRevision {
		n.finish(c, op, errors.New("policy changed"), nil)
		return
	}
	secret, err := selfServiceRandom(32)
	if err != nil {
		n.finish(c, op, err, nil)
		return
	}
	key := "cp-console-" + secret
	next.APIKeys = append(next.APIKeys, key)
	next.AccessIsolation.Keys = append(next.AccessIsolation.Keys, config.ClientKeyBinding{Key: key, Name: name, Pool: body.PoolID, Note: "Console-managed inference channel"})
	if err = next.ValidateAccessIsolation(); err == nil {
		err = config.SaveConfigPreserveComments(h.configFilePath, next)
	}
	if err != nil {
		n.finish(c, op, err, nil)
		return
	}
	h.cfg = next
	h.reloadConfigAfterManagementSaveAsync(c.Request.Context(), h.reloadSnapshotConfigLocked())
	n.finish(c, op, nil, gin.H{"key": key, "pool_id": body.PoolID})
}
