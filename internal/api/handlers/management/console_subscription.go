package management

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"golang.org/x/net/proxy"
)

type consoleSubscription struct {
	key, plan           string
	observed, attempted time.Time
}

type consoleSubscriptions struct {
	mu      sync.Mutex
	entries map[string]consoleSubscription
	running bool
}

type consoleSubscriptionTarget struct {
	id, key, route, bindIP string
	index                  int
	canQuery               bool
}

func newConsoleSubscriptionTarget(cfg *config.Config, auth *coreauth.Auth, account consoleAccount) (consoleSubscriptionTarget, bool) {
	if !account.OAuth || account.LocalIdentity == "" || coreauth.IsPluginVirtualAuth(auth) || coreauth.ValidateConsoleCredential(context.Background(), cfg, auth) != nil {
		return consoleSubscriptionTarget{}, false
	}
	route := strings.TrimSpace(auth.ProxyURL)
	if route == "" {
		route = strings.TrimSpace(cfg.ProxyURL)
	}
	if route == "" && cfg.AccessIsolation.Enabled {
		// Legacy accounts without a lifecycle route can use their unique pool egress.
		for _, pool := range cfg.AccessIsolation.Pools {
			if !slices.Contains(account.Pools, pool.ID) || pool.Egress == nil || pool.Egress.Mode != "direct-ip" {
				continue
			}
			poolRoute := "direct-ip://" + pool.Egress.SourceIP
			if route != "" && route != poolRoute {
				return consoleSubscriptionTarget{}, false
			}
			route = poolRoute
		}
	}
	if route == "" {
		route = "direct"
	}
	var bindIP string
	if auth.Provider == "claude" {
		for _, key := range []string{"bind_ip", "bind-ip"} {
			bindIP = strings.TrimSpace(auth.Attributes[key])
			if bindIP == "" {
				bindIP = claude.ReadMetadataString(&auth.Metadata, key)
			}
			if bindIP != "" {
				break
			}
		}
	}
	key := digestJSON([]any{account.LocalIdentity, account.Email, auth.RegistrationEpoch, auth.CredentialVersion, route, bindIP})
	return consoleSubscriptionTarget{id: auth.ID, key: key, route: route, bindIP: bindIP, canQuery: !auth.Disabled && auth.HasValidAccessToken(time.Now())}, true
}

func (n *consoleNode) annotateSubscriptions(targets []consoleSubscriptionTarget, accounts []consoleAccount) {
	cache := &n.subscriptions
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.entries == nil {
		cache.entries = make(map[string]consoleSubscription)
	}
	now := time.Now()
	active := make(map[string]bool, len(targets))
	var due []consoleSubscriptionTarget
	for _, target := range targets {
		active[target.id] = true
		fact := cache.entries[target.id]
		if fact.key != target.key {
			fact = consoleSubscription{key: target.key}
			cache.entries[target.id] = fact
		}
		if fact.plan != "" && now.Sub(fact.observed) < 24*time.Hour {
			accounts[target.index].SubscriptionPlan = fact.plan
			accounts[target.index].SubscriptionObservedAt = fact.observed.UTC().Format(time.RFC3339)
		}
		if !cache.running && target.canQuery && now.Sub(fact.observed) >= time.Hour && now.Sub(fact.attempted) >= 15*time.Minute {
			due = append(due, target)
		}
	}
	for id := range cache.entries {
		if !active[id] {
			delete(cache.entries, id)
		}
	}
	if len(due) > 0 {
		cache.running = true
		go n.refreshSubscriptions(due)
	}
}

func (n *consoleNode) refreshSubscriptions(targets []consoleSubscriptionTarget) {
	cache := &n.subscriptions
	defer func() {
		cache.mu.Lock()
		cache.running = false
		cache.mu.Unlock()
	}()
	for _, target := range targets {
		runtime := n.h.snapshotAuthFileRuntime()
		auth, ok := runtime.manager.GetByID(target.id)
		if !ok || auth.Disabled || !auth.HasValidAccessToken(time.Now()) {
			continue
		}
		current, ok := newConsoleSubscriptionTarget(runtime.cfg, auth, n.account(auth))
		if !ok || current.key != target.key {
			continue
		}
		plan, verified := target.query(auth)
		now := time.Now()
		cache.mu.Lock()
		if fact := cache.entries[target.id]; fact.key == target.key {
			fact.attempted = now
			if verified {
				fact.plan = plan
				fact.observed = now
			}
			cache.entries[target.id] = fact
		}
		cache.mu.Unlock()
	}
}

func (target consoleSubscriptionTarget) query(auth *coreauth.Auth) (string, bool) {
	route, errParse := proxyutil.Parse(target.route)
	if errParse != nil || route.Mode == proxyutil.ModeInherit {
		return "", false
	}
	var dialer proxy.Dialer
	if target.bindIP != "" {
		ip := net.ParseIP(target.bindIP)
		if ip == nil || ip.IsUnspecified() || (route.Mode == proxyutil.ModeDirectIP && route.URL.Host != ip.String()) {
			return "", false
		}
		dialer = &net.Dialer{LocalAddr: &net.TCPAddr{IP: ip}}
	}
	transport, _, errTransport := proxyutil.BuildHTTPTransportWithBase(target.route, dialer)
	if errTransport != nil || transport == nil {
		return "", false
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return queryConsoleSubscription(client, auth)
}

// The provider replies stay on the original node. Only the allowlisted plan and
// its observation time are copied to the private Console inventory.
func queryConsoleSubscription(client *http.Client, auth *coreauth.Auth) (string, bool) {
	token, _ := auth.Metadata["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", false
	}
	endpoint := claude.ProfileURL
	if auth.Provider == "codex" {
		endpoint = "https://chatgpt.com/backend-api/wham/usage"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if errRequest != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
	if auth.Provider == "claude" {
		req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
		req.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
	} else {
		accountID, _ := auth.Metadata["account_id"].(string)
		if accountID = strings.TrimSpace(accountID); accountID == "" {
			return "", false
		}
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	const maxBody = 1 << 20
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if errRead != nil || len(body) > maxBody {
		return "", false
	}
	return parseConsoleSubscription(auth, body)
}

func parseConsoleSubscription(auth *coreauth.Auth, body []byte) (string, bool) {
	if auth.Provider == "codex" {
		var usage struct {
			Plan string `json:"plan_type"`
		}
		if json.Unmarshal(body, &usage) != nil || usage.Plan == "" {
			return "", false
		}
		switch plan := strings.ToLower(strings.TrimSpace(usage.Plan)); plan {
		case "free", "go", "plus", "pro", "prolite", "team", "business", "enterprise", "edu":
			return plan, true
		case "pro-lite", "pro_lite":
			return "prolite", true
		default:
			return "", true
		}
	}
	var profile struct {
		Account *struct {
			ID    string `json:"uuid"`
			Email string `json:"email"`
			Max   *bool  `json:"has_claude_max"`
			Pro   *bool  `json:"has_claude_pro"`
		} `json:"account"`
		Organization struct {
			Type   string `json:"organization_type"`
			Tier   string `json:"rate_limit_tier"`
			Status string `json:"subscription_status"`
		} `json:"organization"`
	}
	if json.Unmarshal(body, &profile) != nil || profile.Account == nil {
		return "", false
	}
	id := claude.ReadMetadataString(&auth.Metadata, "account_uuid")
	email := claude.ReadMetadataString(&auth.Metadata, "email")
	if id != "" && profile.Account.ID != "" {
		if profile.Account.ID != id {
			return "", false
		}
	} else if email == "" || !strings.EqualFold(strings.TrimSpace(profile.Account.Email), email) {
		return "", false
	}
	max, pro, org := profile.Account.Max, profile.Account.Pro, profile.Organization
	switch {
	case max != nil && *max:
		switch org.Tier {
		case "default_claude_max_5x":
			return "max-5x", true
		case "default_claude_max_20x":
			return "max-20x", true
		default:
			return "max", true
		}
	case pro != nil && *pro:
		return "pro", true
	case org.Status == "active" && org.Type == "claude_team":
		return "team", true
	case org.Status == "active" && org.Type == "claude_enterprise":
		return "enterprise", true
	case max != nil && !*max && pro != nil && !*pro && (org.Type == "" || org.Type == "claude_free"):
		return "free", true
	default:
		return "", true
	}
}
