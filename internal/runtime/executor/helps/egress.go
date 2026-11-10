package helps

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
)

var poolHTTPTransports = NewTransportCache[string](DefaultTransportCacheCapacity)

type egressTransportError struct{ err error }

func (e egressTransportError) RoundTrip(*http.Request) (*http.Response, error) { return nil, e.err }

func poolHTTPTransport(ctx context.Context) (http.RoundTripper, bool) {
	route := proxyutil.EgressFromContext(ctx)
	if route.SourceIP == "" {
		return nil, false
	}
	transport, err := poolHTTPTransports.Get(route.Key(), func() (*http.Transport, error) {
		dialer, errDialer := proxyutil.NewSourceIPDialer(route.SourceIP)
		if errDialer != nil {
			return nil, errDialer
		}
		transport, _, errBuild := proxyutil.BuildHTTPTransportWithBase("direct", dialer)
		if errBuild != nil {
			return nil, errBuild
		}
		if transport.TLSClientConfig != nil && transport.TLSClientConfig.ClientSessionCache != nil {
			// Transport.Clone retains the cache interface from the default transport.
			transport.TLSClientConfig.ClientSessionCache = tls.NewLRUClientSessionCache(0)
		}
		transport.TLSHandshakeTimeout = 0
		transport.ResponseHeaderTimeout = 0
		transport.ExpectContinueTimeout = 0
		transport.IdleConnTimeout = 0
		return transport, nil
	})
	if err != nil {
		return egressTransportError{err: err}, true
	}
	return transport, true
}

func newPoolUtlsHTTPClient(ctx context.Context, auth *cliproxyauth.Auth, timeout time.Duration) (*http.Client, bool) {
	standard, bound := poolHTTPTransport(ctx)
	if !bound {
		return nil, false
	}
	route := proxyutil.EgressFromContext(ctx)
	dialer, err := proxyutil.NewSourceIPDialer(route.SourceIP)
	if err != nil {
		return &http.Client{Transport: egressTransportError{err: err}}, true
	}
	key := claudeCodeTransportKey{
		Owner: claudeTransportOwner(auth) + ":" + route.Key(), ProxyURL: "direct",
		BindIP: route.SourceIP, Origin: claudeTransportOrigin(auth),
	}
	anthropic := claudeCodeRoundTripperCache.GetOrAdd(key, func() http.RoundTripper {
		return newClaudeCodeRoundTripperWithDialer(dialer)
	})
	return &http.Client{Timeout: timeout, Transport: &fallbackRoundTripper{
		anthropic: anthropic,
		chrome:    &utlsRoundTripper{dialer: dialer},
		fallback:  standard,
	}}, true
}

// ApplyPoolWebsocketEgress returns true when an explicit pool route overrides
// credential, global, and environment proxies. Errors never restore a default dialer.
func ApplyPoolWebsocketEgress(ctx context.Context, dialer *websocket.Dialer) bool {
	route := proxyutil.EgressFromContext(ctx)
	if route.SourceIP == "" {
		return false
	}
	dialer.Proxy = nil
	dialer.NetDialTLSContext = nil
	source, err := proxyutil.NewSourceIPDialer(route.SourceIP)
	if err != nil {
		dialer.NetDialContext = func(context.Context, string, string) (net.Conn, error) { return nil, err }
	} else {
		dialer.NetDialContext = source.DialContext
	}
	return true
}
