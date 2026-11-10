package proxyutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
)

type egressContextKey struct{}

// Egress is a request-owned direct route. An empty SourceIP inherits existing routing.
type Egress struct {
	Scope    string
	SourceIP string
}

func WithEgress(ctx context.Context, route Egress) context.Context {
	return context.WithValue(ctx, egressContextKey{}, route)
}

func EgressFromContext(ctx context.Context) Egress {
	if ctx == nil {
		return Egress{}
	}
	route, _ := ctx.Value(egressContextKey{}).(Egress)
	return route
}

// Key partitions persistent connections by both pool and source address.
func (e Egress) Key() string {
	if e.SourceIP == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(e.Scope + "\x00" + e.SourceIP))
	return hex.EncodeToString(digest[:])
}

// EgressError must not penalize a credential or retry through another route.
type EgressError struct{ Err error }

func (e *EgressError) Error() string       { return "pool IPv4 egress unavailable: " + e.Err.Error() }
func (e *EgressError) Unwrap() error       { return e.Err }
func (*EgressError) StatusCode() int       { return 503 }
func (*EgressError) IsRequestScoped() bool { return true }

type SourceIPDialer struct{ dialer net.Dialer }

func NewSourceIPDialer(sourceIP string) (*SourceIPDialer, error) {
	ip, err := netip.ParseAddr(sourceIP)
	if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return nil, &EgressError{Err: fmt.Errorf("invalid IPv4 source address %q", sourceIP)}
	}
	return &SourceIPDialer{dialer: net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IP(ip.AsSlice())}}}, nil
}

func (d *SourceIPDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if network != "tcp" && network != "tcp4" {
		return nil, &EgressError{Err: fmt.Errorf("source IPv4 cannot dial network %q", network)}
	}
	conn, err := d.dialer.DialContext(ctx, "tcp4", address)
	if err != nil {
		return nil, &EgressError{Err: err}
	}
	return conn, nil
}

func (d *SourceIPDialer) Dial(network, address string) (net.Conn, error) {
	return d.DialContext(context.Background(), network, address)
}
