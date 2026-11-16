package auth

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	"testing"
)

func TestConsoleBindingFailsClosedWithoutChangingLegacy(t *testing.T) {
	b := &ConsoleBinding{NodeID: "one", InstanceID: "generation", ProfileID: "fixed", ProfileVersion: "1", ProxyURL: "direct-ip://127.0.0.1"}
	cfg := &config.Config{ConsoleNode: config.ConsoleNodeConfig{Enabled: true, NodeID: b.NodeID, InstanceID: b.InstanceID, EgressProfiles: []config.ConsoleEgressProfile{{ID: b.ProfileID, Version: b.ProfileVersion, ProxyURL: b.ProxyURL}}}}
	a := &Auth{ProxyURL: b.ProxyURL, Metadata: map[string]any{"console_binding": b}}
	if e := ValidateConsoleCredential(context.Background(), cfg, a); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(){func() { cfg.ConsoleNode.InstanceID = "other" }, func() { cfg.ConsoleNode.EgressProfiles[0].Version = "other" }, func() { a.ProxyURL = "direct" }, func() { a.Metadata["bind_ip"] = "127.0.0.1" }} {
		old := cfg.CloneForRuntime()
		oldA := a.Clone()
		change()
		e := ValidateConsoleCredential(context.Background(), cfg, a)
		if e == nil || !isRequestScopedError(e) {
			t.Fatal("mismatch allowed or credential cooldown error")
		}
		cfg = old
		a = oldA
	}
	if e := ValidateConsoleCredential(proxyutil.WithEgress(context.Background(), proxyutil.Egress{SourceIP: "127.0.0.2"}), cfg, a); e == nil {
		t.Fatal("pool override allowed")
	}
	if e := ValidateConsoleCredential(context.Background(), nil, &Auth{}); e != nil {
		t.Fatal("legacy behavior changed")
	}
}
