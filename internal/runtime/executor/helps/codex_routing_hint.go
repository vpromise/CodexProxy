package helps

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
)

// ApplyCodexOAuthRoutingHint aligns OAuth headers with the resolved model and
// final request tier. Callers exclude API-key auth and apply model overrides last.
// Operator header rules take precedence over the derived hint. A reused WebSocket
// keeps its original handshake; changing tiers does not force a reconnect.
func ApplyCodexOAuthRoutingHint(ctx context.Context, headers http.Header, attrs map[string]string, baseModel string, upstreamBody []byte, clientHeaders http.Header) {
	const name = "X-Codex-Routing-Hint"
	for key := range headers {
		if strings.EqualFold(key, name) {
			delete(headers, key)
		}
	}
	if len(attrs) > 0 {
		resolved := (&http.Request{Header: http.Header{}}).WithContext(ctx)
		util.ApplyCustomHeadersFromAttrs(resolved, attrs, clientHeaders)
		if value := strings.TrimSpace(resolved.Header.Get(name)); value != "" {
			headers.Set(name, value)
			return
		}
	}
	model := strings.TrimSpace(baseModel)
	if model == "" {
		return
	}
	hint := "model=" + model
	if tier := gjson.GetBytes(upstreamBody, "service_tier"); tier.Type == gjson.String {
		if value := strings.TrimSpace(tier.String()); value != "" {
			hint += ";tier=" + value
		}
	}
	headers.Set(name, hint)
}
