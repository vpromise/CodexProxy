package management

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestConsoleSubscriptionLegacyEgressAndCredentialChange(t *testing.T) {
	auth := &coreauth.Auth{ID: "credential", Provider: "claude", Metadata: map[string]any{"access_token": "fixture-token"}}
	account := consoleAccount{OAuth: true, LocalIdentity: "account", Pools: []string{"one", "two"}}
	cfg := &config.Config{}
	cfg.AccessIsolation.Enabled = true
	cfg.AccessIsolation.Pools = []config.AccountPool{
		{ID: "one", Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "192.0.2.1"}},
		{ID: "two", Egress: &config.PoolEgress{Mode: "direct-ip", SourceIP: "192.0.2.2"}},
	}
	if _, ok := newConsoleSubscriptionTarget(cfg, auth, account); ok {
		t.Fatal("accepted ambiguous pool egress")
	}
	account.Pools = []string{"one"}
	target, ok := newConsoleSubscriptionTarget(cfg, auth, account)
	if !ok || target.route != "direct-ip://192.0.2.1" {
		t.Fatalf("unexpected route: %q", target.route)
	}
	// Keep the tests offline by using a disabled account when reading the cache.
	auth.Disabled = true
	target.canQuery = false
	n := &consoleNode{subscriptions: consoleSubscriptions{entries: map[string]consoleSubscription{
		auth.ID: {key: target.key, plan: "pro", observed: time.Now()},
	}}}
	rows := []consoleAccount{account}
	n.annotateSubscriptions([]consoleSubscriptionTarget{target}, rows)
	if rows[0].SubscriptionPlan != "pro" || rows[0].SubscriptionObservedAt == "" {
		t.Fatalf("verified fact missing: %#v", rows[0])
	}
	auth.CredentialVersion++
	changed, _ := newConsoleSubscriptionTarget(cfg, auth, account)
	rows[0] = account
	n.annotateSubscriptions([]consoleSubscriptionTarget{changed}, rows)
	if rows[0].SubscriptionPlan != "" {
		t.Fatal("published a fact from a previous credential version")
	}
}

type consoleSubscriptionRoundTrip func(*http.Request) (*http.Response, error)

func (f consoleSubscriptionRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConsoleSubscriptionRequestsAndAllowsOnlyVerifiedPlan(t *testing.T) {
	for _, tc := range []struct {
		name, provider, host, path, body, want string
		verified                               bool
	}{
		{"claude max", "claude", "api.anthropic.com", "/api/oauth/profile", `{"account":{"uuid":"owner","has_claude_max":true},"organization":{"rate_limit_tier":"default_claude_max_5x"}}`, "max-5x", true},
		{"claude wrong owner", "claude", "api.anthropic.com", "/api/oauth/profile", `{"account":{"uuid":"other","email":"same@example.test","has_claude_pro":true}}`, "", false},
		{"codex pro lite", "codex", "chatgpt.com", "/backend-api/wham/usage", `{"plan_type":"pro_lite","rate_limit":{"private":true}}`, "prolite", true},
		{"codex unknown", "codex", "chatgpt.com", "/backend-api/wham/usage", `{"plan_type":"unrecognized"}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: consoleSubscriptionRoundTrip(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet || r.URL.Host != tc.host || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer fixture-token" {
					t.Fatal("unexpected subscription request")
				}
				if tc.provider == "codex" && r.Header.Get("Chatgpt-Account-Id") != "workspace" {
					t.Fatal("Codex subscription request missing workspace")
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			auth := &coreauth.Auth{Provider: tc.provider, Metadata: map[string]any{"access_token": "fixture-token", "account_id": "workspace", "account_uuid": "owner", "email": "same@example.test"}}
			plan, verified := queryConsoleSubscription(client, auth)
			if plan != tc.want || verified != tc.verified {
				t.Fatalf("got %q, %v, want %q, %v", plan, verified, tc.want, tc.verified)
			}
		})
	}
}
