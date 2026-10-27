package auth

import (
	"context"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"testing"
)

func TestHomeUsageSnapshotPreservesAccessIdentity(t *testing.T) {
	ctx := WithClientIdentity(context.Background(), "access-client", "codex")
	ctx = coreusage.CaptureAPIKey(ctx)
	setHomeUserAPIKeyOnGinContext(ctx, "home-usage-client")
	if key, ok := coreusage.CapturedAPIKeyFromContext(ctx); !ok || key != "home-usage-client" {
		t.Fatalf("Home snapshot = %q, %v", key, ok)
	}
	identity := ctx.Value(clientIdentityKey{}).(clientIdentity)
	if identity.key != "access-client" || identity.provider != "codex" {
		t.Fatalf("Home usage update changed access grant: %#v", identity)
	}
}
