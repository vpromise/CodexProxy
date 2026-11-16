package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type selfServiceSummary struct {
	ID          string     `json:"id"`
	Provider    string     `json:"provider"`
	Label       string     `json:"label"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	SubmittedAt *time.Time `json:"submitted_at,omitempty"`
	ReviewedAt  *time.Time `json:"reviewed_at,omitempty"`
	Email       string     `json:"email,omitempty"`
	AccountID   string     `json:"account_id,omitempty"`
	FileName    string     `json:"file_name,omitempty"`
}

func selfServiceSummarize(r *selfServiceRecord) selfServiceSummary {
	status := r.Status
	if (status == "invited" || status == "authorizing") && time.Now().After(r.ExpiresAt) {
		status = "expired"
	}
	return selfServiceSummary{r.ID, r.Provider, r.Label, status, r.CreatedAt, r.ExpiresAt, r.SubmittedAt, r.ReviewedAt, r.Email, r.AccountID, r.FileName}
}

func selfServiceHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
}

func selfServiceBody(c *gin.Context, out any) bool {
	selfServiceHeaders(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 8<<10)
	if err := c.ShouldBindJSON(out); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return false
	}
	return true
}

func selfServicePublicResponse(c *gin.Context, r *selfServiceRecord) {
	status := r.Status
	switch status {
	case "pending", "importing", "accepted", "rejected":
		status = "received"
	case "exchanging":
		status = "processing"
	}
	c.JSON(http.StatusOK, gin.H{"provider": r.Provider, "status": status, "expires_at": r.ExpiresAt})
}

// This bearer authorizes only this enrollment, never management or inference.
func selfServicePublicRecord(c *gin.Context, dir string) (*selfServiceRecord, bool) {
	selfServiceHeaders(c)
	if origin := c.GetHeader("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || parsed.Host != c.Request.Host || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "origin rejected"})
			return nil, false
		}
	}
	header := c.GetHeader("Authorization")
	token, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invitation unavailable"})
		return nil, false
	}
	record, err := selfServiceAuthenticate(dir, token)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invitation unavailable"})
		return nil, false
	}
	return record, true
}

func (h *Handler) ListSelfServiceOAuth(c *gin.Context) {
	selfServiceHeaders(c)
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	dir, err := h.selfServiceDir()
	if err != nil {
		c.JSON(503, gin.H{"error": "intake storage unavailable"})
		return
	}
	records, err := selfServiceList(dir)
	if err != nil {
		c.JSON(503, gin.H{"error": "intake storage unavailable"})
		return
	}
	summaries := make([]selfServiceSummary, 0, len(records))
	for _, r := range records {
		summaries = append(summaries, selfServiceSummarize(r))
	}
	sort.Slice(summaries, func(i, j int) bool { return summaries[i].CreatedAt.After(summaries[j].CreatedAt) })
	c.JSON(200, gin.H{"records": summaries})
}

func (h *Handler) CreateSelfServiceOAuth(c *gin.Context) {
	var request struct {
		Provider string `json:"provider"`
		Label    string `json:"label"`
		TTLHours int    `json:"ttl_hours"`
	}
	if !selfServiceBody(c, &request) {
		return
	}
	if request.Provider != "claude" && request.Provider != "codex" {
		c.JSON(400, gin.H{"error": "unsupported provider"})
		return
	}
	if request.TTLHours == 0 {
		request.TTLHours = 24
	}
	if request.TTLHours < 1 || request.TTLHours > 168 || len(request.Label) > 200 {
		c.JSON(400, gin.H{"error": "invalid invitation settings"})
		return
	}
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	dir, err := h.selfServiceDir()
	if err != nil {
		c.JSON(503, gin.H{"error": "intake storage unavailable"})
		return
	}
	records, err := selfServiceList(dir)
	if err != nil || len(records) >= selfServiceRecordLimit {
		c.JSON(503, gin.H{"error": "intake storage unavailable or full"})
		return
	}
	id, err := selfServiceRandom(16)
	if err != nil {
		c.Status(500)
		return
	}
	secret, err := selfServiceRandom(32)
	if err != nil {
		c.Status(500)
		return
	}
	hash := sha256.Sum256([]byte(secret))
	now := time.Now()
	record := &selfServiceRecord{ID: id, Provider: request.Provider, Label: strings.TrimSpace(request.Label), Status: "invited", CreatedAt: now, ExpiresAt: now.Add(time.Duration(request.TTLHours) * time.Hour), TokenHash: hex.EncodeToString(hash[:])}
	if err = selfServiceWrite(dir, record); err != nil {
		c.JSON(503, gin.H{"error": "could not save invitation"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"record": selfServiceSummarize(record), "invitation_token": id + "." + secret})
}

func (h *Handler) GetSelfServiceOAuth(c *gin.Context) {
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	dir, err := h.selfServiceDir()
	if err != nil {
		c.Status(503)
		return
	}
	record, ok := selfServicePublicRecord(c, dir)
	if !ok {
		return
	}
	// A process interruption cannot safely replay a possibly consumed grant.
	// Bound the waiting page and require a new invitation instead.
	if record.Status == "exchanging" && time.Since(record.ExchangeAt) > 2*time.Minute {
		record.Status = "failed"
		record.clearFlow()
		if err = selfServiceWrite(dir, record); err != nil {
			c.Status(503)
			return
		}
	}
	selfServicePublicResponse(c, record)
}

func (h *Handler) StartSelfServiceOAuth(c *gin.Context) {
	var request struct {
		Consent bool `json:"consent"`
	}
	if !selfServiceBody(c, &request) {
		return
	}
	if !request.Consent {
		c.JSON(400, gin.H{"error": "consent required"})
		return
	}
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	dir, err := h.selfServiceDir()
	if err != nil {
		c.Status(503)
		return
	}
	record, ok := selfServicePublicRecord(c, dir)
	if !ok {
		return
	}
	if record.Status == "invited" {
		cfg, cfgErr := h.selfServiceRecordConfig(record)
		if cfgErr != nil {
			c.Status(503)
			return
		}
		if err = selfServiceStart(cfg, record); err != nil {
			c.JSON(503, gin.H{"error": "authorization unavailable"})
			return
		}
		record.Status = "authorizing"
		now := time.Now()
		record.ConsentAt = &now
		record.FlowExpiresAt = now.Add(30 * time.Minute)
		if err = selfServiceWrite(dir, record); err != nil {
			c.Status(503)
			return
		}
	}
	if record.Status != "authorizing" || time.Now().After(record.FlowExpiresAt) {
		c.JSON(409, gin.H{"error": "authorization already used or expired"})
		return
	}
	c.JSON(200, gin.H{"url": record.AuthURL})
}

func (h *Handler) CompleteSelfServiceOAuth(c *gin.Context) {
	var request struct {
		Callback string `json:"callback"`
	}
	if !selfServiceBody(c, &request) {
		return
	}
	selfServiceMu.Lock()
	dir, err := h.selfServiceDir()
	if err != nil {
		selfServiceMu.Unlock()
		c.Status(503)
		return
	}
	record, ok := selfServicePublicRecord(c, dir)
	if !ok {
		selfServiceMu.Unlock()
		return
	}
	if record.Status != "authorizing" || time.Now().After(record.FlowExpiresAt) {
		selfServiceMu.Unlock()
		c.JSON(409, gin.H{"error": "authorization already used or expired"})
		return
	}
	code, err := selfServiceCallback(request.Callback, record)
	if err != nil {
		selfServiceMu.Unlock()
		c.JSON(400, gin.H{"error": "invalid callback or state"})
		return
	}
	record.Status = "exchanging"
	record.ExchangeAt = time.Now()
	if err = selfServiceWrite(dir, record); err != nil {
		selfServiceMu.Unlock()
		c.Status(503)
		return
	}
	cfg, cfgErr := h.selfServiceRecordConfig(record)
	if cfgErr != nil {
		record.Status = "failed"
		record.clearFlow()
		_ = selfServiceWrite(dir, record)
		selfServiceMu.Unlock()
		c.Status(503)
		return
	}
	selfServiceMu.Unlock()

	// Finish saving a consumed grant even if the visitor closes the browser.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 90*time.Second)
	defer cancel()
	exchange := h.selfServiceExchange
	if exchange == nil {
		exchange = selfServiceExchange
	}
	credentials, exchangeErr := exchange(ctx, cfg, record, code)
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	current, err := selfServiceRead(dir, record.ID)
	if err != nil {
		c.Status(503)
		return
	}
	// Revocation during an in-flight exchange always wins.
	if current.Status != "exchanging" {
		c.JSON(409, gin.H{"error": "authorization closed"})
		return
	}
	current.clearFlow()
	if exchangeErr != nil || credentials == nil {
		current.Status = "failed"
		if err = selfServiceWrite(dir, current); err != nil {
			c.Status(503)
			return
		}
		c.JSON(502, gin.H{"error": "authorization failed; request a new invitation"})
		return
	}
	now := time.Now()
	current.Status = "pending"
	current.SubmittedAt = &now
	current.Credentials = credentials
	current.Credentials["type"] = current.Provider
	current.Email, _ = credentials["email"].(string)
	current.AccountID, _ = credentials["account_id"].(string)
	if current.Provider == "claude" {
		current.AccountID, _ = credentials["account_uuid"].(string)
	}
	if err = selfServiceWrite(dir, current); err != nil {
		c.Status(503)
		return
	}
	selfServicePublicResponse(c, current)
}

func (h *Handler) RejectSelfServiceOAuth(c *gin.Context) {
	selfServiceHeaders(c)
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	dir, err := h.selfServiceDir()
	if err != nil {
		c.Status(503)
		return
	}
	record, err := selfServiceRead(dir, c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": "record not found"})
		return
	}
	if record.Status == "accepted" || record.Status == "importing" {
		c.JSON(409, gin.H{"error": "already imported or import in progress"})
		return
	}
	now := time.Now()
	record.ReviewedAt = &now
	if record.Status == "pending" {
		record.Status = "rejected"
	} else {
		record.Status = "revoked"
	}
	record.clearFlow()
	record.Credentials = nil
	if err = selfServiceWrite(dir, record); err != nil {
		c.Status(503)
		return
	}
	c.JSON(200, gin.H{"record": selfServiceSummarize(record)})
}

func selfServiceSameAccount(a, b map[string]any, provider string) bool {
	key := "account_id"
	if provider == "claude" {
		key = "account_uuid"
	}
	aID, _ := a[key].(string)
	bID, _ := b[key].(string)
	if aID != "" && bID != "" {
		return aID == bID
	}
	aEmail, _ := a["email"].(string)
	bEmail, _ := b["email"].(string)
	return aEmail != "" && strings.EqualFold(aEmail, bEmail)
}

func selfServiceDuplicate(runtime authFileRuntime, record *selfServiceRecord) (bool, error) {
	if runtime.manager != nil {
		for _, auth := range runtime.manager.List() {
			if auth != nil && auth.Provider == record.Provider && auth.FileName != record.FileName && selfServiceSameAccount(auth.Metadata, record.Credentials, record.Provider) {
				return true, nil
			}
		}
	}
	entries, err := os.ReadDir(runtime.cfg.AuthDir)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == record.FileName || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(runtime.cfg.AuthDir, entry.Name()))
		if err != nil {
			return false, err
		}
		var metadata map[string]any
		if err = json.Unmarshal(data, &metadata); err != nil {
			return false, err
		}
		if metadata["type"] == record.Provider && selfServiceSameAccount(metadata, record.Credentials, record.Provider) {
			return true, nil
		}
	}
	return false, nil
}

func (h *Handler) AcceptSelfServiceOAuth(c *gin.Context) {
	selfServiceHeaders(c)
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	h.authStatusMu.Lock()
	defer h.authStatusMu.Unlock()
	dir, err := h.selfServiceDir()
	if err != nil {
		c.Status(503)
		return
	}
	record, err := selfServiceRead(dir, c.Param("id"))
	if err != nil {
		c.JSON(404, gin.H{"error": "record not found"})
		return
	}
	if record.Status == "accepted" {
		c.JSON(200, gin.H{"record": selfServiceSummarize(record)})
		return
	}
	if record.Status != "pending" && record.Status != "importing" {
		c.JSON(409, gin.H{"error": "no pending credentials"})
		return
	}
	if record.AccountID == "" && record.Email == "" {
		c.JSON(409, gin.H{"error": "account identity unavailable; authorize again"})
		return
	}
	runtime := h.snapshotAuthFileRuntime()
	if runtime.manager == nil {
		c.JSON(503, gin.H{"error": "auth manager unavailable"})
		return
	}
	record.FileName = "self-service-" + record.Provider + "-" + record.ID + ".json"
	duplicate, err := selfServiceDuplicate(runtime, record)
	if err != nil {
		c.JSON(503, gin.H{"error": "could not verify existing accounts"})
		return
	}
	if duplicate {
		c.JSON(409, gin.H{"error": "account already exists; existing credentials preserved"})
		return
	}
	record.Status = "importing"
	if err = selfServiceWrite(dir, record); err != nil {
		c.Status(503)
		return
	}
	target := filepath.Join(runtime.cfg.AuthDir, record.FileName)
	data, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		// All provider fields are preserved. The existing enable/pool workflow remains
		// the only way to make this credential usable after administrator acceptance.
		metadata := make(map[string]any, len(record.Credentials)+2)
		for key, value := range record.Credentials {
			metadata[key] = value
		}
		metadata["disabled"] = true
		metadata["self_service_id"] = record.ID
		if record.Console != nil {
			metadata["console_binding"] = record.Console
			metadata["proxy_url"] = record.Console.ProxyURL
		}
		data, err = json.Marshal(metadata)
		if err == nil {
			err = runtime.writeAuthFile(c.Request.Context(), record.FileName, data)
		}
	} else if err == nil {
		// Resume a partially completed import without overwriting newer tokens,
		// administrator edits, disabled state or pool identity.
		var metadata map[string]any
		err = json.Unmarshal(data, &metadata)
		if err == nil && metadata["self_service_id"] != record.ID {
			err = errors.New("account file collision")
		}
		if err == nil {
			var authErr error
			auth, buildErr := runtime.buildAuthFromFileData(target, data)
			if buildErr != nil {
				authErr = buildErr
			} else {
				authErr = runtime.upsertAuthRecord(c.Request.Context(), auth)
			}
			err = authErr
			if err == nil && runtime.postPersistHook != nil {
				err = runtime.postPersistHook(c.Request.Context(), auth)
			}
		}
	}
	if err != nil {
		c.JSON(503, gin.H{"error": "import incomplete; retry acceptance"})
		return
	}
	now := time.Now()
	record.Status = "accepted"
	record.ReviewedAt = &now
	record.Credentials = nil
	if err = selfServiceWrite(dir, record); err != nil {
		c.Status(503)
		return
	}
	c.JSON(200, gin.H{"record": selfServiceSummarize(record)})
}

// Kept injectable per handler for offline regression tests; production always
// uses the same provider implementation as the existing OAuth login paths.
type selfServiceExchangeFunc func(context.Context, *config.Config, *selfServiceRecord, string) (map[string]any, error)

// selfServiceRecordConfig pins managed enrollment to its immutable egress binding.
func (h *Handler) selfServiceRecordConfig(record *selfServiceRecord) (*config.Config, error) {
	cfg := h.snapshotAuthFileRuntime().cfg
	if record.Console != nil {
		fake := &coreauth.Auth{ProxyURL: record.Console.ProxyURL, Metadata: map[string]any{"console_binding": record.Console}}
		if err := coreauth.ValidateConsoleCredential(context.Background(), cfg, fake); err != nil {
			return nil, err
		}
		cfg.ProxyURL = record.Console.ProxyURL
	}
	return cfg, nil
}
