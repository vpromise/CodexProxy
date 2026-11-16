package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"strings"
)

// ConsoleBinding travels with a credential, never in public model or intake responses.
type ConsoleBinding struct {
	NodeID         string `json:"node_id"`
	InstanceID     string `json:"instance_id"`
	ProfileID      string `json:"profile_id"`
	ProfileVersion string `json:"profile_version"`
	ProxyURL       string `json:"proxy_url"`
}

func ConsoleCredentialBinding(a *Auth) (*ConsoleBinding, error) {
	if a == nil || a.Metadata == nil {
		return nil, nil
	}
	value, exists := a.Metadata["console_binding"]
	if !exists {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("invalid console binding")
	}
	var b ConsoleBinding
	if json.Unmarshal(raw, &b) != nil || b.NodeID == "" || b.InstanceID == "" || b.ProfileID == "" || b.ProfileVersion == "" || b.ProxyURL == "" {
		return nil, fmt.Errorf("invalid console binding")
	}
	return &b, nil
}

// ValidateConsoleCredential rejects stale owners and routes before any upstream call.
func ValidateConsoleCredential(ctx context.Context, cfg *config.Config, a *Auth) error {
	b, err := ConsoleCredentialBinding(a)
	if err != nil {
		return consoleBindingError()
	}
	if b == nil {
		return nil
	}
	if cfg == nil || !cfg.ConsoleNode.Enabled || cfg.Home.Enabled || b.NodeID != cfg.ConsoleNode.NodeID || b.InstanceID != cfg.ConsoleNode.InstanceID || a.ProxyURL != b.ProxyURL {
		return consoleBindingError()
	}
	matched := false
	for _, p := range cfg.ConsoleNode.EgressProfiles {
		if p.ID == b.ProfileID && p.Version == b.ProfileVersion && p.ProxyURL == b.ProxyURL {
			matched = true
			break
		}
	}
	setting, err := proxyutil.Parse(b.ProxyURL)
	if !matched || err != nil || (setting.Mode != proxyutil.ModeProxy && setting.Mode != proxyutil.ModeDirectIP) {
		return consoleBindingError()
	}
	route := proxyutil.EgressFromContext(ctx)
	if route.SourceIP != "" && (setting.Mode != proxyutil.ModeDirectIP || setting.URL.Host != route.SourceIP) {
		return consoleBindingError()
	}
	// Additional per-account binding would change the explicitly managed route.
	for _, key := range []string{"bind_ip", "bind-ip"} {
		if strings.TrimSpace(a.Attributes[key]) != "" {
			return consoleBindingError()
		}
		if value, _ := a.Metadata[key].(string); strings.TrimSpace(value) != "" {
			return consoleBindingError()
		}
	}
	return nil
}
func consoleBindingError() error {
	return NewRequestScopedError("console_route_unavailable: Managed account owner or egress changed; administrator reconciliation required", 503)
}
