package auth

import (
	"context"
	"crypto/sha256"
)

type skipPersistContextKey struct{}
type deferAPIKeyModelAliasRebuildContextKey struct{}
type refreshPersistenceContextKey struct{}

type refreshPersistenceSource struct {
	path              string
	credential        [sha256.Size]byte
	pendingCredential [sha256.Size]byte
}

func withRefreshPersistence(ctx context.Context, current *Auth, pending *localCodexRefreshSave) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	source := refreshPersistenceSource{path: localCodexAuthPath(current)}
	if source.path != "" {
		source.credential = codexFileCredentialDigest(current)
		if pending.matches(current) {
			source.pendingCredential = pending.diskCredential
		}
	}
	return context.WithValue(ctx, refreshPersistenceContextKey{}, source)
}

// IsRefreshPersistence reports whether a token store is saving an OAuth
// refresh result from the manager.
func IsRefreshPersistence(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(refreshPersistenceContextKey{}).(refreshPersistenceSource)
	return ok
}

// IsLocalCodexRefresh reports that a local file save belongs to the native
// Codex OAuth registration used for this refresh.
func IsLocalCodexRefresh(ctx context.Context, auth *Auth) bool {
	if ctx == nil {
		return false
	}
	source, ok := ctx.Value(refreshPersistenceContextKey{}).(refreshPersistenceSource)
	return ok && source.path != "" && source.path == localCodexAuthPath(auth)
}

// MatchesLocalCodexRefreshSource checks that the observed file still holds the
// credential used for refresh or the previously recorded unpersisted version.
func MatchesLocalCodexRefreshSource(ctx context.Context, raw []byte) bool {
	if ctx == nil {
		return false
	}
	source, ok := ctx.Value(refreshPersistenceContextKey{}).(refreshPersistenceSource)
	previous := parseLocalCodexJSON(raw)
	if !ok || source.path == "" || previous == nil {
		return false
	}
	digest := codexFileCredentialDigest(previous)
	return digest == source.credential || digest == source.pendingCredential
}

// WithSkipPersist returns a derived context that disables persistence for Manager Update/Register calls.
// It is intended for code paths that are reacting to file watcher events, where the file on disk is
// already the source of truth and persisting again would create a write-back loop.
func WithSkipPersist(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skipPersistContextKey{}, true)
}

func shouldSkipPersist(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(skipPersistContextKey{})
	enabled, ok := v.(bool)
	return ok && enabled
}

// WithDeferredAPIKeyModelAliasRebuild returns a derived context that defers API-key model alias table rebuilds.
// Callers that use this for a batch of Register/Update/Remove operations must call RefreshAPIKeyModelAlias once.
func WithDeferredAPIKeyModelAliasRebuild(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deferAPIKeyModelAliasRebuildContextKey{}, true)
}

func shouldDeferAPIKeyModelAliasRebuild(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(deferAPIKeyModelAliasRebuildContextKey{})
	enabled, ok := v.(bool)
	return ok && enabled
}
