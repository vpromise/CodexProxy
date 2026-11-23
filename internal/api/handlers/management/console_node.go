package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/fileperm"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// consoleNode is the private, versioned control adapter. It never exposes a
// general management proxy and owns no second provider refresh implementation.
type consoleNode struct {
	h            *Handler
	cfg          config.ConsoleNodeConfig
	authDir, dir string
	legacy       *gin.Engine
	mu           sync.Mutex
}
type consoleOperation struct {
	ID           string `json:"id"`
	Kind         string `json:"kind"`
	Digest       string `json:"digest"`
	EnrollmentID string `json:"enrollment_id,omitempty"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
}
type consoleEnrollment struct {
	RenewalOf      string          `json:"renewal_of,omitempty"`
	ID             string          `json:"id"`
	Provider       string          `json:"provider"`
	Status         string          `json:"status"`
	Email          string          `json:"email,omitempty"`
	Principal      string          `json:"principal,omitempty"`
	Organization   string          `json:"organization,omitempty"`
	ProfileID      string          `json:"profile_id"`
	ProfileVersion string          `json:"profile_version"`
	ExpiresAt      time.Time       `json:"expires_at"`
	NeedsReauth    bool            `json:"needs_reauth"`
	TokenExpires   string          `json:"token_expires,omitempty"`
	Account        *consoleAccount `json:"account,omitempty"`
}
type consoleAccount struct {
	OAuth         bool     `json:"oauth"`
	EnrollmentID  string   `json:"enrollment_id,omitempty"`
	Provider      string   `json:"provider"`
	Principal     string   `json:"principal,omitempty"`
	Email         string   `json:"email,omitempty"`
	LocalIdentity string   `json:"local_identity,omitempty"`
	Disabled      bool     `json:"disabled"`
	Registered    bool     `json:"registered"`
	Managed       bool     `json:"managed"`
	Revision      string   `json:"revision"`
	ProfileID     string   `json:"profile_id,omitempty"`
	Pools         []string `json:"pools"`
}

// NewConsoleNodeServer is opt-in and returns a dedicated mutual-TLS listener.
// The normal management/inference listener and its authentication stay unchanged.
func (h *Handler) NewConsoleNodeServer(cfg *config.Config) (*http.Server, error) {
	if cfg == nil || !cfg.ConsoleNode.Enabled {
		return nil, nil
	}
	c := cfg.ConsoleNode
	if !selfServiceID(c.InstanceID) || c.NodeID == "" || len(c.NodeID) > 80 || c.Listen == "" || cfg.Home.Enabled {
		return nil, errors.New("console node identity/listen invalid or Home enabled")
	}
	host, _, err := net.SplitHostPort(c.Listen)
	if err != nil || net.ParseIP(host) == nil || (!net.ParseIP(host).IsPrivate() && !net.ParseIP(host).IsLoopback()) {
		return nil, errors.New("console node listen must use an explicit private or loopback IP")
	}
	origin, err := url.Parse(c.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, errors.New("console public origin must be an HTTPS origin")
	}
	if len(c.ConsoleCertificateSHA256) != 64 {
		return nil, errors.New("console certificate SHA256 required")
	}
	if _, err = hex.DecodeString(c.ConsoleCertificateSHA256); err != nil {
		return nil, errors.New("invalid console certificate pin")
	}
	if len(c.EgressProfiles) == 0 {
		return nil, errors.New("console node requires an explicit egress profile")
	}
	seen := map[string]bool{}
	for _, p := range c.EgressProfiles {
		if p.ID == "" || p.Version == "" || seen[p.ID] {
			return nil, errors.New("console egress profiles require unique IDs and versions")
		}
		seen[p.ID] = true
		parsed, err := proxyutil.Parse(p.ProxyURL)
		if err != nil || parsed.Mode == proxyutil.ModeInherit || parsed.Mode == proxyutil.ModeDirect {
			return nil, errors.New("console egress requires a proxy or direct-ip route")
		}
		if _, _, err = proxyutil.BuildDialer(p.ProxyURL); err != nil {
			return nil, errors.New("invalid console egress")
		}
	}
	pair, err := tls.LoadX509KeyPair(c.Cert, c.Key)
	if err != nil {
		return nil, errors.New("cannot load console node TLS keypair")
	}
	ca, err := os.ReadFile(c.ClientCA)
	if err != nil {
		return nil, errors.New("cannot read console client CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid console client CA")
	}
	authDir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err != nil {
		return nil, err
	}
	authDir, err = filepath.Abs(authDir)
	if err != nil {
		return nil, err
	}
	n := &consoleNode{h: h, cfg: c, authDir: authDir, dir: authDir + ".console-node", legacy: gin.New()}
	if err = fileperm.EnsurePrivateDir(n.dir); err != nil {
		return nil, err
	}
	// Only fixed internal calls are installed; the capture router is not a listener.
	n.legacy.POST("/intake/:id/accept", h.AcceptSelfServiceOAuth)
	n.legacy.POST("/intake/:id/reject", h.RejectSelfServiceOAuth)
	n.legacy.GET("/session", h.GetSelfServiceOAuth)
	n.legacy.POST("/start", h.StartSelfServiceOAuth)
	n.legacy.POST("/complete", h.CompleteSelfServiceOAuth)
	engine := gin.New()
	engine.Use(gin.Recovery(), n.guard)
	engine.GET("/v1/capabilities", n.capabilities)
	engine.GET("/v1/inventory", n.inventory)
	engine.GET("/v1/enrollments", n.list)
	engine.POST("/v1/enrollments", n.create)
	engine.GET("/v1/enrollments/:id", n.get)
	engine.DELETE("/v1/enrollments/:id", n.deleteInvitation)
	engine.POST("/v1/enrollments/:id/:action", n.action)
	engine.GET("/v1/operations/:id", n.operation)
	engine.POST("/v1/channel-keys", n.channelKey)
	return &http.Server{Addr: c.Listen, Handler: engine, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, NextProtos: []string{"h2", "http/1.1"}}}, nil
}
func (n *consoleNode) guard(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	if c.Request.TLS == nil || len(c.Request.TLS.VerifiedChains) == 0 || len(c.Request.TLS.PeerCertificates) == 0 {
		c.AbortWithStatus(401)
		return
	}
	pin := sha256.Sum256(c.Request.TLS.PeerCertificates[0].Raw)
	if hex.EncodeToString(pin[:]) != strings.ToLower(n.cfg.ConsoleCertificateSHA256) {
		c.AbortWithStatus(403)
		return
	}
	cfg := n.h.snapshotAuthFileRuntime().cfg
	dir, err := util.ResolveAuthDir(cfg.AuthDir)
	if err == nil {
		dir, err = filepath.Abs(dir)
	}
	if err != nil || dir != n.authDir || !reflect.DeepEqual(n.cfg, cfg.ConsoleNode) || cfg.Home.Enabled {
		c.AbortWithStatusJSON(503, gin.H{"error": "node_configuration_changed_restart_required"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	c.Next()
}
func (n *consoleNode) capabilities(c *gin.Context) {
	profiles := []map[string]string{}
	for _, p := range n.cfg.EgressProfiles {
		profile := map[string]string{"id": p.ID, "version": p.Version, "expected_public_ip": p.ExpectedPublicIP}
		if route, err := proxyutil.Parse(p.ProxyURL); err == nil {
			switch route.Mode {
			case proxyutil.ModeDirectIP:
				profile["egress_mode"] = "direct-ip"
				profile["source_ip"] = route.URL.Host
			case proxyutil.ModeProxy:
				profile["egress_mode"] = "proxy"
			}
		}
		profiles = append(profiles, profile)
	}
	c.JSON(200, gin.H{"protocol": "console-node/v1", "node_id": n.cfg.NodeID, "instance_id": n.cfg.InstanceID, "profiles": profiles, "features": []string{"enrollment", "disabled_import", "pool_assignment", "versioned_enable", "operation_receipts", "scoped_channel_keys", "owner_renewal", "invitation_deletion"}})
}
func digestJSON(v any) string {
	raw, _ := json.Marshal(v)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func consolePrincipal(meta map[string]any, provider string) string {
	if provider == "claude" {
		s, _ := meta["account_uuid"].(string)
		return strings.TrimSpace(s)
	}
	// AccountID is a workspace identity, not a global person identity.
	token, _ := meta["id_token"].(string)
	if claims, err := codex.ParseJWTToken(token); err == nil {
		for _, s := range []string{claims.CodexAuthInfo.UserID, claims.CodexAuthInfo.ChatgptUserID, claims.Sub} {
			if strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
func (n *consoleNode) inventory(c *gin.Context) {
	cfg := n.h.snapshotAuthFileRuntime().cfg
	accounts := []consoleAccount{}
	for _, a := range n.h.snapshotAuthFileRuntime().manager.List() {
		if a.Provider == "claude" || a.Provider == "codex" {
			accounts = append(accounts, n.account(a))
		}
	}
	pools := []map[string]any{}
	for _, p := range cfg.AccessIsolation.Pools {
		pools = append(pools, map[string]any{"id": p.ID, "name": p.Name, "provider": p.Provider, "egress": p.Egress})
	}
	c.JSON(200, gin.H{"accounts": accounts, "pools": pools, "isolation_enabled": cfg.AccessIsolation.Enabled, "policy_revision": n.policyRevision()})
}
func (n *consoleNode) read(id string) (*selfServiceRecord, error) {
	if !selfServiceID(id) {
		return nil, os.ErrNotExist
	}
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	r, err := selfServiceRead(n.authDir+".self-service-oauth", id)
	if err == nil && !n.ownsInvitation(r) {
		return nil, os.ErrNotExist
	}
	return r, err
}
func (n *consoleNode) ownsInvitation(r *selfServiceRecord) bool {
	return r.Console != nil && r.Console.NodeID == n.cfg.NodeID && r.Console.InstanceID == n.cfg.InstanceID
}
func (n *consoleNode) deleteInvitation(c *gin.Context) {
	deleteSelfServiceInvitation(c, n.authDir+".self-service-oauth", n.ownsInvitation)
}
func (n *consoleNode) get(c *gin.Context) {
	r, err := n.read(c.Param("id"))
	if err != nil {
		c.Status(404)
		return
	}
	c.JSON(200, n.summary(r))
}
func (n *consoleNode) list(c *gin.Context) {
	selfServiceMu.Lock()
	rows, err := selfServiceList(n.authDir + ".self-service-oauth")
	selfServiceMu.Unlock()
	if err != nil {
		c.Status(503)
		return
	}
	out := []consoleEnrollment{}
	for _, r := range rows {
		if n.ownsInvitation(r) {
			out = append(out, n.summary(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	c.JSON(200, gin.H{"records": out})
}
func atomicConsoleWrite(path string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".control-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (n *consoleNode) operation(c *gin.Context) {
	id := c.Param("id")
	if !selfServiceID(id) {
		c.Status(404)
		return
	}
	data, err := os.ReadFile(filepath.Join(n.dir, id+".json"))
	if err != nil {
		c.Status(404)
		return
	}
	var op consoleOperation
	if json.Unmarshal(data, &op) != nil {
		c.Status(503)
		return
	}
	c.JSON(200, op)
}
func (n *consoleNode) begin(c *gin.Context, kind, id, enrollment string, body any) (*consoleOperation, bool) {
	if !selfServiceID(id) {
		c.JSON(422, gin.H{"error": "invalid_operation_id"})
		return nil, false
	}
	p := filepath.Join(n.dir, id+".json")
	raw, err := os.ReadFile(p)
	want := digestJSON(body)
	if err == nil {
		var op consoleOperation
		if json.Unmarshal(raw, &op) != nil {
			c.Status(503)
			return nil, false
		}
		if op.Kind != kind || op.Digest != want {
			c.JSON(409, gin.H{"error": "operation_payload_changed"})
			return nil, false
		}
		c.JSON(200, gin.H{"operation": op})
		return nil, false
	}
	if !os.IsNotExist(err) {
		c.Status(503)
		return nil, false
	}
	entries, err := os.ReadDir(n.dir)
	if err != nil || len(entries) >= 10000 {
		c.JSON(503, gin.H{"error": "operation_storage_full"})
		return nil, false
	}
	op := &consoleOperation{ID: id, Kind: kind, Digest: want, EnrollmentID: enrollment, Status: "uncertain"}
	if atomicConsoleWrite(p, op) != nil {
		c.Status(503)
		return nil, false
	}
	return op, true
}
func (n *consoleNode) finish(c *gin.Context, op *consoleOperation, err error, extra gin.H) {
	op.Status = "succeeded"
	if err != nil {
		op.Status = "failed"
		op.Error = "operation_failed_reconcile_before_retry"
		switch err.Error() {
		case "needs reauthorization", "needs reauthorization on original owner":
			op.Error = "needs_reauth_on_original_owner"
		case "principal unavailable":
			op.Error = "verified_identity_required"
		case "account changed":
			op.Error = "revision_changed_refresh_first"
		case "prepare an isolated pool first":
			op.Error = "isolated_provider_pool_required"
		case "pool egress conflicts", "pool exit changed", "pool egress changed":
			op.Error = "pool_egress_conflict"
		}
	}
	if atomicConsoleWrite(filepath.Join(n.dir, op.ID+".json"), op) != nil {
		c.JSON(503, gin.H{"error": "operation_receipt_uncertain"})
		return
	}
	if extra == nil {
		extra = gin.H{}
	}
	extra["operation"] = op
	if err != nil {
		c.JSON(409, extra)
	} else {
		c.JSON(200, extra)
	}
}
func (n *consoleNode) create(c *gin.Context) {
	var body struct {
		OperationID      string `json:"operation_id"`
		RenewalOf        string `json:"renewal_of"`
		ExpectedRevision string `json:"expected_revision"`
		Provider         string `json:"provider"`
		ProfileID        string `json:"profile_id"`
		Label            string `json:"label"`
		TTLHours         int    `json:"ttl_hours"`
	}
	if c.ShouldBindJSON(&body) != nil || (body.Provider != "claude" && body.Provider != "codex") || len(body.Label) > 200 {
		c.Status(422)
		return
	}
	var profile *config.ConsoleEgressProfile
	for i := range n.cfg.EgressProfiles {
		if n.cfg.EgressProfiles[i].ID == body.ProfileID {
			profile = &n.cfg.EgressProfiles[i]
		}
	}
	if profile == nil {
		c.JSON(422, gin.H{"error": "unknown_profile"})
		return
	}
	if body.RenewalOf != "" {
		old, e := n.read(body.RenewalOf)
		if e != nil || old.Status != "accepted" || old.RenewalOf != "" {
			c.JSON(409, gin.H{"error": "original_owner_record_required"})
			return
		}
		a := n.findAuth(old)
		if a == nil || !a.Disabled || n.account(a).Revision != body.ExpectedRevision || old.Provider != body.Provider || !reflect.DeepEqual(old.Console, &coreauth.ConsoleBinding{NodeID: n.cfg.NodeID, InstanceID: n.cfg.InstanceID, ProfileID: profile.ID, ProfileVersion: profile.Version, ProxyURL: profile.ProxyURL}) {
			c.JSON(409, gin.H{"error": "disable_original_account_and_refresh_revision_first"})
			return
		}
	}
	if body.TTLHours == 0 {
		body.TTLHours = 24
	}
	if body.TTLHours < 1 || body.TTLHours > 168 {
		c.Status(422)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	op, ok := n.begin(c, "create", body.OperationID, body.OperationID, body)
	if !ok {
		return
	}
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	dir := n.authDir + ".self-service-oauth"
	rows, err := selfServiceList(dir)
	if err != nil || len(rows) >= selfServiceRecordLimit {
		n.finish(c, op, errors.New("intake full"), nil)
		return
	}
	if _, err = selfServiceRead(dir, body.OperationID); !os.IsNotExist(err) {
		n.finish(c, op, errors.New("intake exists"), nil)
		return
	}
	secret, err := selfServiceRandom(32)
	if err != nil {
		n.finish(c, op, err, nil)
		return
	}
	hash := sha256.Sum256([]byte(secret))
	now := time.Now()
	row := &selfServiceRecord{RenewalOf: body.RenewalOf, RenewalRevision: body.ExpectedRevision, ID: body.OperationID, Provider: body.Provider, Label: body.Label, Status: "invited", CreatedAt: now, ExpiresAt: now.Add(time.Duration(body.TTLHours) * time.Hour), TokenHash: hex.EncodeToString(hash[:]), Console: &coreauth.ConsoleBinding{NodeID: n.cfg.NodeID, InstanceID: n.cfg.InstanceID, ProfileID: profile.ID, ProfileVersion: profile.Version, ProxyURL: profile.ProxyURL}}
	err = selfServiceWrite(dir, row)
	n.finish(c, op, err, gin.H{"record": n.summary(row), "invitation_token": row.ID + "." + secret})
}

type consoleCapture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (w *consoleCapture) Header() http.Header { return w.header }
func (w *consoleCapture) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *consoleCapture) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(data)
}
func (n *consoleNode) local(ctx context.Context, method, path string, body any, bearer string) (int, []byte) {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, method, "http://local"+path, bytes.NewReader(raw))
	origin, _ := url.Parse(n.cfg.PublicOrigin)
	req.Host = origin.Host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", n.cfg.PublicOrigin)
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	w := &consoleCapture{header: http.Header{}}
	n.legacy.ServeHTTP(w, req)
	return w.status, w.body.Bytes()
}
func (n *consoleNode) action(c *gin.Context) {
	id, action := c.Param("id"), c.Param("action")
	r, err := n.read(id)
	if err != nil {
		c.Status(404)
		return
	}
	if action == "session" || action == "start" || action == "complete" {
		if origin := c.GetHeader("Origin"); origin != "" && origin != n.cfg.PublicOrigin {
			c.Status(403)
			return
		}
		var body map[string]any
		if action != "session" && c.ShouldBindJSON(&body) != nil {
			c.Status(400)
			return
		}
		method := http.MethodPost
		if action == "session" {
			method = http.MethodGet
		}
		status, data := n.local(c.Request.Context(), method, "/"+action, body, c.GetHeader("Authorization"))
		c.Data(status, "application/json", data)
		return
	}
	var body struct {
		OperationID      string `json:"operation_id"`
		PoolID           string `json:"pool_id"`
		ExpectedRevision string `json:"expected_revision"`
	}
	if c.ShouldBindJSON(&body) != nil {
		c.Status(422)
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	op, ok := n.begin(c, action, body.OperationID, id, []any{id, body})
	if !ok {
		return
	}
	switch action {
	case "reject":
		status, _ := n.local(c.Request.Context(), http.MethodPost, "/intake/"+id+"/reject", gin.H{}, "")
		if status != 200 {
			err = errors.New("reject failed")
		}
	case "assign":
		err = n.assign(c.Request.Context(), r, body.PoolID, body.ExpectedRevision)
	case "enable", "disable":
		err = n.setStatus(c.Request.Context(), r, body.ExpectedRevision, action == "disable")
	default:
		err = errors.New("unknown action")
	}
	n.finish(c, op, err, nil)
}
func (n *consoleNode) findAuth(r *selfServiceRecord) *coreauth.Auth {
	if r == nil || r.FileName == "" || filepath.Base(r.FileName) != r.FileName {
		return nil
	}
	manager := n.h.snapshotAuthFileRuntime().manager
	if manager == nil {
		return nil
	}
	rootID := consoleRootID(r)
	var match *coreauth.Auth
	for _, a := range manager.List() {
		if a.Provider != r.Provider || a.Metadata["self_service_id"] != rootID {
			continue
		}
		if a.FileName != r.FileName {
			// Some loaded registrations keep the file identity in ID and path.
			// Require both and the original node binding before using that identity.
			name := strings.TrimSpace(a.FileName)
			if (name != "" && name != r.FileName) || a.ID != r.FileName || a.Attributes["path"] != filepath.Join(n.authDir, r.FileName) {
				continue
			}
			binding, err := coreauth.ConsoleCredentialBinding(a)
			if err != nil || binding == nil || !reflect.DeepEqual(binding, r.Console) {
				continue
			}
		}
		if match != nil {
			return nil
		}
		match = a
	}
	return match
}
func consoleRootID(r *selfServiceRecord) string {
	if r.RenewalOf != "" {
		return r.RenewalOf
	}
	return r.ID
}
func (n *consoleNode) assign(ctx context.Context, r *selfServiceRecord, poolID, expected string) error {
	if r.RenewalOf != "" {
		return n.renew(ctx, r)
	}
	if r.Status != "pending" && r.Status != "importing" && r.Status != "accepted" {
		return errors.New("not submitted")
	}
	if r.Status != "accepted" {
		if consolePrincipal(r.Credentials, r.Provider) == "" || coreauth.AccountSubject(&coreauth.Auth{Provider: r.Provider, Metadata: r.Credentials}) == "" {
			return errors.New("principal unavailable")
		}
		expiry, _ := r.Credentials["expired"].(string)
		if tm, e := time.Parse(time.RFC3339, expiry); e != nil || !tm.After(time.Now()) {
			return errors.New("needs reauthorization")
		}
	}
	cfg := n.h.snapshotAuthFileRuntime().cfg
	var pool *config.AccountPool
	for i := range cfg.AccessIsolation.Pools {
		if cfg.AccessIsolation.Pools[i].ID == poolID {
			pool = &cfg.AccessIsolation.Pools[i]
		}
	}
	if !cfg.AccessIsolation.Enabled || pool == nil || pool.Provider != r.Provider {
		return errors.New("prepare an isolated pool first")
	}
	if pool.Egress != nil && pool.Egress.Mode == "direct-ip" && r.Console.ProxyURL != "direct-ip://"+pool.Egress.SourceIP {
		return errors.New("pool egress conflicts")
	}
	if r.Status == "accepted" {
		a := n.findAuth(r)
		if a == nil || n.account(a).Revision != expected || !a.Disabled {
			return errors.New("account changed")
		}
	}
	status, _ := n.local(ctx, http.MethodPost, "/intake/"+r.ID+"/accept", gin.H{}, "")
	if status != 200 {
		return errors.New("import incomplete")
	}
	fresh, err := n.read(r.ID)
	if err != nil {
		return err
	}
	a := n.findAuth(fresh)
	if a == nil || !a.Disabled {
		return errors.New("import not ready")
	}
	h := n.h
	h.authStatusMu.Lock()
	defer h.authStatusMu.Unlock()
	h.mu.Lock()
	defer h.mu.Unlock()
	current, ok := h.authManager.GetByID(a.ID)
	if !ok || !current.Disabled {
		return errors.New("account changed")
	}
	identity := h.persistedPoolIdentity(current)
	if identity == "" {
		return errors.New("identity not ready")
	}
	// Re-read and validate the latest policy under the management save lock.
	next, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		return err
	}
	found := false
	for i := range next.AccessIsolation.Pools {
		p := &next.AccessIsolation.Pools[i]
		if p.ID != poolID {
			continue
		}
		if p.Provider != r.Provider {
			return errors.New("pool changed")
		}
		if p.Egress != nil && p.Egress.Mode == "direct-ip" && r.Console.ProxyURL != "direct-ip://"+p.Egress.SourceIP {
			return errors.New("pool exit changed")
		}
		found = true
		exists := false
		for _, id := range p.Accounts {
			exists = exists || id == identity
		}
		if !exists {
			p.Accounts = append(p.Accounts, identity)
		}
	}
	if !found || !next.AccessIsolation.Enabled {
		return errors.New("pool removed")
	}
	if err = h.validatePoolAccounts(next); err != nil {
		return err
	}
	if err = config.SaveConfigPreserveComments(h.configFilePath, next); err != nil {
		return err
	}
	h.cfg = next
	snapshot := h.reloadSnapshotConfigLocked()
	h.reloadConfigAfterManagementSaveAsync(ctx, snapshot)
	return nil
}
func (n *consoleNode) setStatus(ctx context.Context, r *selfServiceRecord, expected string, disabled bool) error {
	if r.Status != "accepted" {
		return errors.New("not accepted")
	}
	h := n.h
	h.authStatusMu.Lock()
	defer h.authStatusMu.Unlock()
	a := n.findAuth(r)
	if a == nil || expected == "" || n.account(a).Revision != expected {
		return errors.New("account changed")
	}
	if _, e := os.Stat(filepath.Join(n.authDir, filepath.Base(r.FileName))); e != nil {
		return errors.New("account changed")
	}
	if !disabled {
		expiry, _ := a.Metadata["expired"].(string)
		if tm, e := time.Parse(time.RFC3339, expiry); e != nil || !tm.After(time.Now()) {
			return errors.New("needs reauthorization on original owner")
		}
		d := n.account(a)
		cfg := h.snapshotAuthFileRuntime().cfg
		for _, pool := range cfg.AccessIsolation.Pools {
			for _, id := range d.Pools {
				if pool.ID == id && (pool.Provider != r.Provider || (pool.Egress != nil && pool.Egress.Mode == "direct-ip" && r.Console.ProxyURL != "direct-ip://"+pool.Egress.SourceIP)) {
					return errors.New("pool egress changed")
				}
			}
		}
		if !cfg.AccessIsolation.Enabled || len(d.Pools) == 0 {
			return errors.New("assign a pool first")
		}
		if _, err := h.selfServiceRecordConfig(r); err != nil {
			return err
		}
	}
	updated, err := h.authManager.PatchAuth(ctx, a.ID, a.RegistrationEpoch, func(latest *coreauth.Auth) error {
		if latest.Generation != a.Generation || latest.CredentialVersion != a.CredentialVersion {
			return errors.New("account changed")
		}
		applyAuthDisabledState(latest, disabled)
		return nil
	})
	if err != nil {
		return err
	}
	if h.postAuthPersistHook != nil {
		return h.postAuthPersistHook(ctx, updated)
	}
	return nil
}

// ServeConsoleNode starts a prevalidated listener with mutual TLS.
func ServeConsoleNode(server *http.Server, listener net.Listener) error {
	err := server.Serve(tls.NewListener(listener, server.TLSConfig))
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (n *consoleNode) account(a *coreauth.Auth) consoleAccount {
	h := n.h
	h.mu.Lock()
	identity := h.persistedPoolIdentity(a)
	cfg := h.cfg.CloneForRuntime()
	policyRevision, _ := h.accessRevision()
	h.mu.Unlock()
	pools := []string{}
	if cfg != nil {
		for _, p := range cfg.AccessIsolation.Pools {
			for _, id := range p.Accounts {
				if id == identity && identity != "" {
					pools = append(pools, p.ID)
				}
			}
		}
	}
	sort.Strings(pools)
	binding, err := coreauth.ConsoleCredentialBinding(a)
	managed := binding != nil && err == nil
	profileID := ""
	if managed {
		profileID = binding.ProfileID
	}
	enrollment, _ := a.Metadata["self_service_id"].(string)
	email, _ := a.Metadata["email"].(string)
	rev := digestJSON([]any{a.ID, a.RegistrationEpoch, a.CredentialVersion, a.Generation, a.Disabled, policyRevision, binding})
	return consoleAccount{OAuth: a.AuthKind() == coreauth.AuthKindOAuth, EnrollmentID: enrollment, Provider: a.Provider, Principal: consolePrincipal(a.Metadata, a.Provider), Email: email, LocalIdentity: identity, Disabled: a.Disabled, Registered: len(registry.GetGlobalRegistry().GetModelsForClient(a.ID)) > 0, Managed: managed, Revision: rev, ProfileID: profileID, Pools: pools}
}
func (n *consoleNode) summary(r *selfServiceRecord) consoleEnrollment {
	dto := consoleEnrollment{RenewalOf: r.RenewalOf, ID: r.ID, Provider: r.Provider, Status: selfServiceSummarize(r).Status, Email: r.Email, ExpiresAt: r.ExpiresAt}
	if r.Console != nil {
		dto.ProfileID = r.Console.ProfileID
		dto.ProfileVersion = r.Console.ProfileVersion
	}
	if r.Credentials != nil {
		dto.Principal = consolePrincipal(r.Credentials, r.Provider)
		dto.Organization, _ = r.Credentials["organization_uuid"].(string)
		dto.TokenExpires, _ = r.Credentials["expired"].(string)
	}
	if r.Status == "accepted" || r.Status == "importing" {
		if a := n.findAuth(r); a != nil {
			account := n.account(a)
			dto.Account = &account
			dto.Principal = account.Principal
			dto.Email = account.Email
			dto.TokenExpires, _ = a.Metadata["expired"].(string)
		} else if r.Status == "accepted" {
			if _, err := os.Stat(filepath.Join(n.authDir, filepath.Base(r.FileName))); os.IsNotExist(err) {
				dto.Status = "removed"
			} else {
				dto.Status = "unavailable"
			}
		}
	}
	if r.Status == "pending" || r.Status == "accepted" {
		tm, e := time.Parse(time.RFC3339, dto.TokenExpires)
		dto.NeedsReauth = e != nil || !tm.After(time.Now())
	}
	return dto
}
