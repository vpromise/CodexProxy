package auth

import (
	"context"
	"net/http"
	"strings"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// ClaudeThreadScope partitions ownership and executor snapshots by the current
// authenticated caller, pool, requested model and physical egress selection.
func ClaudeThreadScope(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	apiKey, _ := coreusage.CapturedAPIKeyFromContext(coreusage.CaptureAPIKey(ctx))
	caller := cliproxysession.ClaudeThreadCallerScope(apiKey, opts.Metadata, req.Metadata)
	pool := ""
	if scope := AccessScopeFromContext(ctx); scope.Enabled() {
		pool = scope.pool
	}
	return cliproxysession.CallerScope(caller + "\x00" + pool + "\x00" + requestedModelAliasFromOptions(opts, req.Model) + "\x00" + proxyutil.EgressFromContext(ctx).Key())
}

// withClaudeThreadAuth pins only a successful thread's original credential.
// Current pool grants, availability and credential fences still govern dispatch.
func (m *Manager) withClaudeThreadAuth(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Options, error) {
	if opts.SourceFormat != sdktranslator.FormatClaude || gjson.GetBytes(req.Payload, "thread.type").String() != "continue" {
		return opts, nil
	}
	hasClaude := false
	for _, provider := range providers {
		if strings.EqualFold(provider, "claude") {
			hasClaude = true
			break
		}
	}
	if !hasClaude {
		return opts, nil
	}
	previousID := gjson.GetBytes(req.Payload, "thread.previous_message_id").String()
	owner, found, errOwner := cliproxysession.ClaudeThreadAuth(ctx, ClaudeThreadScope(ctx, req, opts), previousID)
	if errOwner != nil {
		if ctx.Err() != nil {
			return opts, ctx.Err()
		}
		return opts, &Error{Code: "thread_auth_unavailable", Message: "Claude Thread credential binding is temporarily unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	if !found {
		return opts, nil
	}
	if pinned := pinnedAuthIDFromMetadata(opts.Metadata); pinned != "" && pinned != owner {
		return opts, &Error{Code: "thread_auth_conflict", Message: "The pinned credential does not own this Claude Thread; replay with thread create", HTTPStatus: http.StatusConflict}
	}
	opts.Metadata = cloneRequestMetadata(opts.Metadata)
	opts.EnsureMetadata()
	opts.Metadata[cliproxyexecutor.PinnedAuthMetadataKey] = owner
	if errAccess := m.ValidateAccessSelection(ctx, providers, authSelectionModelFromOptions(opts, req.Model), opts); errAccess != nil {
		return opts, errAccess
	}
	return opts, nil
}
