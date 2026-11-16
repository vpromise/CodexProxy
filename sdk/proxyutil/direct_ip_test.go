package proxyutil

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectIPRouteHTTPAndDialer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") {
			t.Errorf("source %s", r.RemoteAddr)
		}
		w.Write([]byte("bound"))
	}))
	defer server.Close()
	tr, mode, e := BuildHTTPTransport("direct-ip://127.0.0.1")
	if e != nil || mode != ModeDirectIP || tr.Proxy != nil {
		t.Fatal("route", e)
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr}
	resp, e := client.Get(server.URL)
	if e != nil {
		t.Fatal(e)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	dialer, _, e := BuildDialer("direct-ip://127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	conn, e := dialer.Dial("tcp", server.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	conn.Close()
	if _, e := NewSourceIPDialer("::1"); e == nil {
		t.Fatal("IPv6 source accepted")
	}
	d, e := NewSourceIPDialer("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = d.DialContext(context.Background(), "tcp6", "[::1]:9"); e == nil {
		t.Fatal("IPv6 destination allowed")
	}
	// A missing source fails locally. No second unbound/direct dial is attempted.
	fail, _, e := BuildHTTPTransport("direct-ip://192.0.2.20")
	if e != nil {
		t.Fatal(e)
	}
	defer fail.CloseIdleConnections()
	if _, e = (&http.Client{Transport: fail}).Get(server.URL); e == nil {
		t.Fatal("missing source silently fell back")
	}
}
func TestDirectIPRejectsAmbiguousRoutes(t *testing.T) {
	for _, raw := range []string{"direct-ip://127.0.0.1:8", "direct-ip://u:p@127.0.0.1", "direct-ip://127.0.0.1/", "direct-ip://127.0.0.1?x=1", "direct-ip://0.0.0.0", "direct-ip://[::1]", "direct-ip://localhost"} {
		if _, e := Parse(raw); e == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}
