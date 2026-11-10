package helps

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

func TestPoolEgressHTTPSCannotUseInheritedLegacyTLSDialer(t *testing.T) {
	for _, factory := range []struct {
		name   string
		client func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client
	}{
		{name: "standard", client: NewProxyAwareHTTPClient},
		{name: "utls-fallback", client: NewUtlsHTTPClient},
	} {
		for _, source := range []string{"127.0.0.1", "203.0.113.250"} {
			t.Run(factory.name+"/"+source, func(t *testing.T) {
				poolHTTPTransports.Purge()
				var hooks, upstreamCalls atomic.Int32
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					upstreamCalls.Add(1)
					host, _, errSplit := net.SplitHostPort(r.RemoteAddr)
					if errSplit != nil || host != source {
						t.Errorf("upstream observed %q, want source %s", r.RemoteAddr, source)
					}
					w.WriteHeader(http.StatusNoContent)
				}))
				t.Cleanup(upstream.Close)
				previous := http.DefaultTransport
				custom := previous.(*http.Transport).Clone()
				// Trust only the local fixture's certificate, preserving TLS validation.
				custom.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
				custom.DialTLSContext = nil
				custom.DialTLS = func(network, address string) (net.Conn, error) {
					hooks.Add(1)
					return tls.Dial(network, address, custom.TLSClientConfig.Clone())
				}
				http.DefaultTransport = custom
				t.Cleanup(func() {
					http.DefaultTransport = previous
					poolHTTPTransports.Purge()
					custom.CloseIdleConnections()
				})
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				defer cancel()
				ctx = proxyutil.WithEgress(ctx, proxyutil.Egress{Scope: t.Name(), SourceIP: source})
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := factory.client(ctx, &config.Config{}, nil, 0).Do(request)
				if response != nil {
					if errClose := response.Body.Close(); errClose != nil {
						t.Fatal(errClose)
					}
				}
				if hooks.Load() != 0 {
					t.Errorf("pool route used inherited TLS dialer %d times", hooks.Load())
				}
				if source == "127.0.0.1" {
					if err != nil || response == nil || response.StatusCode != http.StatusNoContent || upstreamCalls.Load() != 1 {
						t.Fatalf("bound HTTPS failed: response=%v err=%v upstreamCalls=%d", response, err, upstreamCalls.Load())
					}
				} else {
					var routeError *proxyutil.EgressError
					if !errors.As(err, &routeError) || routeError.StatusCode() != http.StatusServiceUnavailable || upstreamCalls.Load() != 0 {
						t.Fatalf("unavailable source bypassed: err=%v upstreamCalls=%d", err, upstreamCalls.Load())
					}
				}
			})
		}
	}
}

func TestPoolEgressTLSSessionsStayWithinPool(t *testing.T) {
	for _, factory := range []struct {
		name   string
		client func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client
	}{
		{name: "standard", client: NewProxyAwareHTTPClient},
		{name: "utls-fallback", client: NewUtlsHTTPClient},
	} {
		t.Run(factory.name, func(t *testing.T) {
			poolHTTPTransports.Purge()
			upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.TLS.DidResume {
					_, _ = io.WriteString(w, "resumed")
				} else {
					_, _ = io.WriteString(w, "fresh")
				}
			}))
			upstream.TLS = &tls.Config{MaxVersion: tls.VersionTLS12}
			upstream.StartTLS()
			t.Cleanup(upstream.Close)
			previous := http.DefaultTransport
			custom := previous.(*http.Transport).Clone()
			custom.TLSClientConfig = upstream.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			custom.TLSClientConfig.MaxVersion = tls.VersionTLS12
			custom.TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(10)
			http.DefaultTransport = custom
			t.Cleanup(func() {
				http.DefaultTransport = previous
				poolHTTPTransports.Purge()
				custom.CloseIdleConnections()
			})
			for _, step := range []struct{ pool, want string }{
				{"one", "fresh"}, {"one", "resumed"}, {"two", "fresh"}, {"two", "resumed"},
			} {
				ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
				ctx = proxyutil.WithEgress(ctx, proxyutil.Egress{Scope: t.Name() + step.pool, SourceIP: "127.0.0.1"})
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream.URL, nil)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				// Force a new connection so the assertion measures TLS resumption, not HTTP reuse.
				request.Close = true
				response, err := factory.client(ctx, nil, nil, 0).Do(request)
				if err != nil {
					cancel()
					t.Fatal(err)
				}
				body, errRead := io.ReadAll(response.Body)
				errClose := response.Body.Close()
				cancel()
				if errRead != nil || errClose != nil {
					t.Fatalf("read response: %v; close: %v", errRead, errClose)
				}
				if string(body) != step.want {
					t.Fatalf("pool %s TLS session = %s, want %s", step.pool, body, step.want)
				}
			}
		})
	}
}
