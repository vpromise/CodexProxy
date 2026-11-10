package proxyutil

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPoolEgressSourceDialer(t *testing.T) {
	for _, source := range []string{"127.0.0.1", "127.0.0.2"} {
		t.Run(source, func(t *testing.T) {
			probe, err := net.Listen("tcp4", net.JoinHostPort(source, "0"))
			if err != nil {
				t.Skipf("source not available on this host: %v", err)
			}
			if errClose := probe.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				host, _, errSplit := net.SplitHostPort(r.RemoteAddr)
				if errSplit != nil || host != source {
					t.Errorf("upstream observed %q, want %q", r.RemoteAddr, source)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			dialer, err := NewSourceIPDialer(source)
			if err != nil {
				t.Fatal(err)
			}
			transport, _, err := BuildHTTPTransportWithBase("direct", dialer)
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			response, err := (&http.Client{Transport: transport}).Get(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if errClose := response.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
		})
	}
}

func TestPoolEgressRejectsInvalidAddressAndIPv6(t *testing.T) {
	for _, source := range []string{"", "example.com", "0.0.0.0", "::1", "224.0.0.1", "255.255.255.255"} {
		if _, err := NewSourceIPDialer(source); err == nil {
			t.Errorf("accepted invalid source %q", source)
		}
	}
	dialer, err := NewSourceIPDialer("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = dialer.DialContext(context.Background(), "tcp6", "[::1]:80")
	var egressErr *EgressError
	if !errors.As(err, &egressErr) || egressErr.StatusCode() != 503 || !egressErr.IsRequestScoped() {
		t.Fatalf("IPv6 fallback not rejected: %v", err)
	}
}
