package helps

import (
	"context"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestConsoleInvalidOwnerNeverCallsAnyHTTPTransport(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer server.Close()
	b := &cliproxyauth.ConsoleBinding{NodeID: "n", InstanceID: "old", ProfileID: "p", ProfileVersion: "1", ProxyURL: "direct-ip://127.0.0.1"}
	a := &cliproxyauth.Auth{ProxyURL: b.ProxyURL, Metadata: map[string]any{"console_binding": b}}
	cfg := &config.Config{ConsoleNode: config.ConsoleNodeConfig{Enabled: true, NodeID: "n", InstanceID: "new"}}
	for _, client := range []*http.Client{NewUtlsHTTPClient(context.Background(), cfg, a, 0), NewProxyAwareHTTPClient(context.Background(), cfg, a, 0)} {
		if _, e := client.Get(server.URL); e == nil {
			t.Fatal("managed mismatch accepted")
		}
	}
	if calls != 0 {
		t.Fatal("request reached fallback")
	}
}
