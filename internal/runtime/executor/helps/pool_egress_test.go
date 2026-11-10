package helps

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

// External verification is opt-in and sends no provider credentials or inference.
func TestPoolEgressPublicIPv4(t *testing.T) {
	configured := os.Getenv("CODEXPROXY_TEST_EGRESS_IPS")
	if configured == "" {
		t.Skip("public egress verification is not enabled")
	}
	for _, source := range strings.Split(configured, ",") {
		t.Run(source, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			ctx = proxyutil.WithEgress(ctx, proxyutil.Egress{Scope: "synthetic-verification-" + source, SourceIP: source})
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := NewProxyAwareHTTPClient(ctx, &config.Config{}, nil, 0).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, errRead := io.ReadAll(response.Body)
			if errClose := response.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if errRead != nil || response.StatusCode != 200 || strings.TrimSpace(string(body)) != source {
				t.Fatalf("public source mismatch: status=%d body=%q err=%v", response.StatusCode, body, errRead)
			}
		})
	}
}

func TestPoolEgressHTTPOverridesAllProxyPaths(t *testing.T) {
	var proxyCalls atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { proxyCalls.Add(1); w.WriteHeader(502) }))
	defer trap.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, errSplit := net.SplitHostPort(r.RemoteAddr)
		if errSplit != nil {
			t.Error(errSplit)
			return
		}
		_, _ = io.WriteString(w, host)
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.ProxyURL = trap.URL
	account := &cliproxyauth.Auth{ID: "synthetic", Provider: "codex", ProxyURL: trap.URL}
	for _, source := range []string{"127.0.0.1", "127.0.0.2"} {
		t.Run(source, func(t *testing.T) {
			probe, err := net.Listen("tcp4", net.JoinHostPort(source, "0"))
			if err != nil {
				t.Skipf("secondary source unavailable: %v", err)
			}
			if errClose := probe.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			ctx := proxyutil.WithEgress(context.Background(), proxyutil.Egress{Scope: "pool-a", SourceIP: source})
			for _, factory := range []func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{NewProxyAwareHTTPClient, NewUtlsHTTPClient} {
				client := factory(ctx, cfg, account, 0)
				for i := 0; i < 2; i++ {
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
					if err != nil {
						t.Fatal(err)
					}
					resp, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					body, errRead := io.ReadAll(resp.Body)
					if errClose := resp.Body.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					if errRead != nil || string(body) != source {
						t.Fatalf("upstream saw %q, want %s: %v", body, source, errRead)
					}
				}
			}
		})
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("pool traffic reached a configured proxy")
	}
	ctxA := proxyutil.WithEgress(context.Background(), proxyutil.Egress{Scope: "a", SourceIP: "127.0.0.1"})
	ctxB := proxyutil.WithEgress(context.Background(), proxyutil.Egress{Scope: "b", SourceIP: "127.0.0.1"})
	a := NewProxyAwareHTTPClient(ctxA, cfg, account, 0)
	if a.Transport == NewProxyAwareHTTPClient(ctxB, cfg, account, 0).Transport {
		t.Fatal("different pools share a transport")
	}
	if a.Transport != NewProxyAwareHTTPClient(ctxA, cfg, account, 0).Transport {
		t.Fatal("same route does not reuse its transport")
	}
}

func TestPoolEgressUnavailableDoesNotFallback(t *testing.T) {
	var reached atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached.Add(1); w.WriteHeader(204) }))
	defer upstream.Close()
	for _, factory := range []func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client{NewProxyAwareHTTPClient, NewUtlsHTTPClient} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ctx = proxyutil.WithEgress(ctx, proxyutil.Egress{Scope: "unavailable", SourceIP: "203.0.113.250"})
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		_, err = factory(ctx, &config.Config{}, nil, 0).Do(request)
		cancel()
		var routeErr *proxyutil.EgressError
		if !errors.As(err, &routeErr) {
			t.Fatalf("missing explicit egress failure: %v", err)
		}
	}
	if reached.Load() != 0 {
		t.Fatal("failed source binding fell back to a usable default source")
	}
}

func TestPoolEgressWebsocketDialer(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.WriteMessage(websocket.TextMessage, []byte(r.RemoteAddr))
	}))
	defer upstream.Close()
	ctx := proxyutil.WithEgress(context.Background(), proxyutil.Egress{Scope: "socket", SourceIP: "127.0.0.1"})
	dialer := &websocket.Dialer{Proxy: func(*http.Request) (*url.URL, error) { t.Error("proxy was used"); return nil, nil }}
	if !ApplyPoolWebsocketEgress(ctx, dialer) {
		t.Fatal("route was not applied")
	}
	conn, _, err := dialer.DialContext(ctx, "ws"+strings.TrimPrefix(upstream.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_, body, err := conn.ReadMessage()
	if err != nil || !strings.HasPrefix(string(body), "127.0.0.1:") {
		t.Fatalf("wrong websocket source: %s %v", body, err)
	}
}
