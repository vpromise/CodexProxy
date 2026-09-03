package helps

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	tls "github.com/refraction-networking/utls"
	claudeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	internalcache "github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/httpwire"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

// utlsRoundTripper implements http.RoundTripper using a Chrome fingerprint for
// providers that require a browser-like TLS and HTTP/2 transport. Each request
// gets a dedicated connection that is closed with the response body.
type utlsRoundTripper struct {
	dialer proxy.Dialer
}

type closeConnectionBody struct {
	io.ReadCloser
	closeConnection func() error
	once            sync.Once
	err             error
}

func (b *closeConnectionBody) Close() error {
	if b == nil {
		return nil
	}
	b.once.Do(func() {
		var errConnection error
		if b.closeConnection != nil {
			errConnection = b.closeConnection()
		}
		var errBody error
		if b.ReadCloser != nil {
			errBody = b.ReadCloser.Close()
		}
		b.err = errors.Join(errBody, errConnection)
	})
	return b.err
}

func newUtlsRoundTripper(proxyURL string) http.RoundTripper {
	var dialer proxy.Dialer = proxy.Direct
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialer(proxyURL)
		if errBuild != nil {
			return claudeTransportError{err: fmt.Errorf("utls: configure proxy dialer %q: %w", proxyutil.Redact(proxyURL), errBuild)}
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}
	return &utlsRoundTripper{dialer: dialer}
}

func (t *utlsRoundTripper) createConnection(ctx context.Context, host, addr string) (*http2.ClientConn, error) {
	contextDialer, ok := t.dialer.(proxy.ContextDialer)
	if !ok {
		return nil, fmt.Errorf("utls: dialer does not support context cancellation")
	}
	conn, errDial := contextDialer.DialContext(ctx, "tcp", addr)
	if errDial != nil {
		return nil, fmt.Errorf("utls: dial upstream: %w", errDial)
	}

	tlsConfig := &tls.Config{ServerName: host}
	tlsConn := tls.UClient(conn, tlsConfig, tls.HelloChrome_Auto)

	if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
		if errors.Is(errHandshake, context.Canceled) || errors.Is(errHandshake, context.DeadlineExceeded) {
			return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
		}
		if errClose := conn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls: TLS handshake: %w; close connection: %v", errHandshake, errClose)
		}
		return nil, fmt.Errorf("utls: TLS handshake: %w", errHandshake)
	}

	tr := &http2.Transport{}
	h2Conn, errClientConn := tr.NewClientConn(tlsConn)
	if errClientConn != nil {
		if errClose := tlsConn.Close(); errClose != nil {
			return nil, fmt.Errorf("utls: initialize HTTP/2 connection: %w; close TLS connection: %v", errClientConn, errClose)
		}
		return nil, fmt.Errorf("utls: initialize HTTP/2 connection: %w", errClientConn)
	}

	return h2Conn, nil
}

func (t *utlsRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	hostname := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(hostname, port)

	h2Conn, err := t.createConnection(req.Context(), hostname, addr)
	if err != nil {
		return nil, err
	}

	resp, err := h2Conn.RoundTrip(req)
	if err != nil {
		if errClose := h2Conn.Close(); errClose != nil {
			log.Debugf("utls: close connection after round trip failure: %v", errClose)
		}
		return nil, err
	}
	if resp == nil {
		if errClose := h2Conn.Close(); errClose != nil {
			log.Debugf("utls: close connection after empty response: %v", errClose)
		}
		return nil, fmt.Errorf("utls: upstream returned an empty response")
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	resp.Body = &closeConnectionBody{
		ReadCloser:      resp.Body,
		closeConnection: h2Conn.Close,
	}
	return resp, nil
}

// claudeCodeSessionCacheCapacity bounds the per-transport TLS session cache for
// the Anthropic inference plane.
const claudeCodeSessionCacheCapacity = 32

// newClaudeCodeTLSConfig builds the uTLS config for one inference-plane dial.
//
// OmitEmptyPsk keeps the pre_shared_key extension silent until a session is
// cached, so an unresumed ClientHello stays byte-identical to the captured
// native handshake. PreferSkipResumptionOnNilExtension turns uTLS's HelloCustom
// "resume without the matching extension" panic into a skipped resumption.
func newClaudeCodeTLSConfig(host string, sessionCache tls.ClientSessionCache) *tls.Config {
	return &tls.Config{
		ServerName:                         host,
		ClientSessionCache:                 sessionCache,
		OmitEmptyPsk:                       true,
		PreferSkipResumptionOnNilExtension: true,
	}
}

// claudeBunGREASEExt emits the fixed GREASE-style extension 0xff01 / len 1 /
// body 0x00 that Bun 1.4.1 (Claude Code 2.1.252) sends in place of the old
// renegotiation_info extension. The body byte is constant across 80 native
// captures, so it is pinned rather than drawn from utls's greaseSeed.
//
// Embedding UtlsGREASEExtension borrows its unexported writeToUConn method
// (required by the TLSExtension write path, unimplementable from another
// package); the type switch `case *UtlsGREASEExtension` in utls's ApplyPreset
// re-greasing loop does not match this wrapper type, so Value is never
// rewritten. Len/Read are overridden to pin the exact bytes.
type claudeBunGREASEExt struct {
	tls.UtlsGREASEExtension
}

func (e *claudeBunGREASEExt) Len() int { return 5 }

func (e *claudeBunGREASEExt) Read(p []byte) (int, error) {
	if len(p) < 5 {
		return 0, errors.New("claudeBunGREASEExt: short buffer")
	}
	copy(p, []byte{0xff, 0x01, 0x00, 0x01, 0x00})
	return 5, io.EOF
}

// claudeCodeTLSClientHelloSpec reproduces the deterministic Bun/BoringSSL
// ClientHello emitted by Claude Code 2.1.252. The template models Bun/BoringSSL,
// not the host operating system. Keep it in sync with a fresh native capture
// whenever the advertised Claude Code version changes. Target JA3:
// d871d02cecbde59abbf8f4806134addf
// (hostname, SNI-first, BoringSSL-padded record, includes padding ext 0x0015)
// / e97f5146a7009cc2918b50e903b6ff8d (IP, no SNI, unpadded).
func claudeCodeTLSClientHelloSpec() *tls.ClientHelloSpec {
	return &tls.ClientHelloSpec{
		CipherSuites: []uint16{
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
		},
		CompressionMethods: []uint8{0},
		Extensions: []tls.TLSExtension{
			&tls.SNIExtension{},
			&tls.ExtendedMasterSecretExtension{},
			// Bun sends a fixed GREASE-style 0xff01 extension here; 2.1.220's
			// renegotiation_info lived at the same position.
			&claudeBunGREASEExt{},
			&tls.SupportedCurvesExtension{Curves: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384}},
			&tls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&tls.SessionTicketExtension{},
			&tls.ALPNExtension{AlpnProtocols: []string{"http/1.1"}},
			&tls.StatusRequestExtension{},
			&tls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []tls.SignatureScheme{
				tls.ECDSAWithP256AndSHA256,
				tls.PSSWithSHA256,
				tls.PKCS1WithSHA256,
				tls.ECDSAWithP384AndSHA384,
				tls.PSSWithSHA384,
				tls.PKCS1WithSHA384,
				tls.PSSWithSHA512,
				tls.PKCS1WithSHA512,
				tls.PKCS1WithSHA1,
			}},
			&tls.SCTExtension{},
			&tls.KeyShareExtension{KeyShares: []tls.KeyShare{{Group: tls.X25519}}},
			&tls.PSKKeyExchangeModesExtension{Modes: []uint8{tls.PskModeDHE}},
			&tls.SupportedVersionsExtension{Versions: []uint16{tls.VersionTLS13, tls.VersionTLS12}},
			// Bun (BoringSSL) pads the ClientHello record to 0x200 (512) when the
			// unpadded length is above 0xff; the SNI extension pushes hostname
			// connections over the threshold, IP connections (251B) stay under
			// it and carry no padding extension in the measured captures.
			&tls.UtlsPaddingExtension{GetPaddingLen: tls.BoringPaddingStyle},
			// pre_shared_key MUST be the final extension (RFC 8446 4.2.11).
			// It contributes zero bytes until a cached session exists.
			&tls.UtlsPreSharedKeyExtension{},
		},
	}
}

const claudeCodeRoundTripperCacheCapacity = 64

type claudeCodeTransportKey struct {
	Owner    string
	ProxyURL string
	BindIP   string
	Origin   string
}

var claudeCodeRoundTripperCache = internalcache.NewBoundedLRU[claudeCodeTransportKey, http.RoundTripper](
	claudeCodeRoundTripperCacheCapacity,
	func(_ claudeCodeTransportKey, roundTripper http.RoundTripper) {
		if transport, ok := roundTripper.(interface{ CloseIdleConnections() }); ok {
			transport.CloseIdleConnections()
		}
	},
)

var claudeStandardRoundTripperCache = internalcache.NewBoundedLRU[claudeCodeTransportKey, http.RoundTripper](
	claudeCodeRoundTripperCacheCapacity,
	func(_ claudeCodeTransportKey, roundTripper http.RoundTripper) {
		if transport, ok := roundTripper.(interface{ CloseIdleConnections() }); ok {
			transport.CloseIdleConnections()
		}
	},
)

var claudeCodeMessagesHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"User-Agent",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"X-Stainless-Timeout",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	"Connection",
	"Host",
	"Accept-Encoding",
	"Content-Length",
}

var claudeCodeCountTokensHeaderOrder = []string{
	"Accept",
	"Authorization",
	"Content-Type",
	"User-Agent",
	"X-Claude-Code-Session-Id",
	"X-Stainless-Arch",
	"X-Stainless-Lang",
	"X-Stainless-OS",
	"X-Stainless-Package-Version",
	"X-Stainless-Retry-Count",
	"X-Stainless-Runtime",
	"X-Stainless-Runtime-Version",
	"anthropic-beta",
	"anthropic-dangerous-direct-browser-access",
	"anthropic-version",
	"x-app",
	"Connection",
	"Host",
	"Accept-Encoding",
	"Content-Length",
}

func claudeCodeRequestHeaderOrder(_, requestTarget string) []string {
	if strings.HasPrefix(requestTarget, "/v1/messages/count_tokens") {
		return claudeCodeCountTokensHeaderOrder
	}
	return claudeCodeMessagesHeaderOrder
}

func cachedClaudeCodeRoundTripper(owner, proxyURL, bindIP, origin string, localIP net.IP) http.RoundTripper {
	key := claudeCodeTransportKey{Owner: owner, ProxyURL: proxyURL, BindIP: bindIP, Origin: origin}
	return claudeCodeRoundTripperCache.GetOrAdd(key, func() http.RoundTripper {
		return newClaudeCodeRoundTripper(proxyURL, localIP)
	})
}

func cachedClaudeStandardRoundTripper(owner, proxyURL, bindIP, origin string, localIP net.IP) http.RoundTripper {
	key := claudeCodeTransportKey{Owner: owner, ProxyURL: proxyURL, BindIP: bindIP, Origin: origin}
	return claudeStandardRoundTripperCache.GetOrAdd(key, func() http.RoundTripper {
		return newClaudeStandardRoundTripper(proxyURL, localIP)
	})
}

func newClaudeCodeRoundTripper(proxyURL string, localIP net.IP) http.RoundTripper {
	// The cache is scoped to this credential/proxy/bind/origin tuple, so neither
	// idle connections nor TLS sessions cross credential or egress boundaries.
	sessionCache := tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity)
	var dialer proxy.Dialer = proxy.Direct
	if localIP != nil {
		dialer = &net.Dialer{LocalAddr: &net.TCPAddr{IP: append(net.IP(nil), localIP...)}}
	}
	if proxyURL != "" {
		proxyDialer, mode, errBuild := proxyutil.BuildDialerWithBase(proxyURL, dialer)
		if errBuild != nil {
			return claudeTransportError{err: fmt.Errorf("claude tls: configure proxy dialer %q: %w", proxyutil.Redact(proxyURL), errBuild)}
		} else if mode != proxyutil.ModeInherit && proxyDialer != nil {
			dialer = proxyDialer
		}
	}

	transport := &http.Transport{
		ForceAttemptHTTP2: false,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var (
				conn net.Conn
				err  error
			)
			if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
				conn, err = contextDialer.DialContext(ctx, network, addr)
			} else {
				conn, err = dialer.Dial(network, addr)
			}
			if err != nil {
				return nil, fmt.Errorf("claude tls: dial upstream: %w", err)
			}

			host, _, errSplit := net.SplitHostPort(addr)
			if errSplit != nil {
				if errClose := conn.Close(); errClose != nil {
					log.Debugf("claude tls: close failed connection: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: split upstream address: %w", errSplit)
			}
			tlsConn := tls.UClient(conn, newClaudeCodeTLSConfig(host, sessionCache), tls.HelloCustom)
			if errPreset := tlsConn.ApplyPreset(claudeCodeTLSClientHelloSpec()); errPreset != nil {
				if errClose := tlsConn.Close(); errClose != nil {
					log.Debugf("claude tls: close connection after preset failure: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: apply Claude Code ClientHello: %w", errPreset)
			}
			if errHandshake := tlsConn.HandshakeContext(ctx); errHandshake != nil {
				if errClose := tlsConn.Close(); errClose != nil {
					log.Debugf("claude tls: close connection after handshake failure: %v", errClose)
				}
				return nil, fmt.Errorf("claude tls: handshake upstream: %w", errHandshake)
			}
			return httpwire.NewOrderedRequestConn(tlsConn, claudeCodeRequestHeaderOrder), nil
		},
	}
	return transport
}

func newClaudeStandardRoundTripper(proxyURL string, localIP net.IP) http.RoundTripper {
	baseDialer := &net.Dialer{}
	if localIP != nil {
		baseDialer.LocalAddr = &net.TCPAddr{IP: append(net.IP(nil), localIP...)}
	}
	transport, mode, errBuild := proxyutil.BuildHTTPTransportWithBase(proxyURL, baseDialer)
	if errBuild != nil {
		return claudeTransportError{err: fmt.Errorf("claude transport: configure proxy %q: %w", proxyutil.Redact(proxyURL), errBuild)}
	}
	if mode != proxyutil.ModeInherit && transport != nil {
		return transport
	}

	// Inherit keeps the default environment proxy behavior while retaining the
	// credential's local source address for direct and proxy connections.
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok || defaultTransport == nil {
		transport = &http.Transport{}
	} else {
		transport = defaultTransport.Clone()
	}
	transport.DialContext = baseDialer.DialContext
	return transport
}

type claudeTransportError struct{ err error }

func (e claudeTransportError) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, e.err
}

func claudeTransportOwner(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return "anonymous"
	}
	identity := strings.TrimSpace(auth.Index)
	if identity == "" {
		identity = strings.TrimSpace(auth.ID)
	}
	if identity == "" {
		identity = strings.TrimSpace(auth.FileName)
	}
	if identity == "" && auth.Attributes != nil {
		identity = strings.TrimSpace(auth.Attributes[cliproxyauth.AttributeSource])
	}
	if identity == "" {
		identity = strings.TrimSpace(auth.Clone().EnsureIndex())
	}
	if identity == "" {
		return "anonymous"
	}
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", sum[:16])
}

func claudeBindIP(auth *cliproxyauth.Auth) (string, net.IP, error) {
	if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "claude") {
		return "", nil, nil
	}
	var raw string
	for _, key := range []string{"bind_ip", "bind-ip"} {
		if auth.Attributes != nil {
			raw = strings.TrimSpace(auth.Attributes[key])
		}
		if raw == "" {
			raw = strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, key))
		}
		if raw != "" {
			break
		}
	}
	if raw == "" {
		return "", nil, nil
	}
	parsed := net.ParseIP(raw)
	if parsed == nil || parsed.IsUnspecified() {
		return "", nil, fmt.Errorf("claude transport: invalid bind IP %q", raw)
	}
	canonical := parsed.String()
	return canonical, parsed, nil
}

func claudeTransportOrigin(auth *cliproxyauth.Auth) string {
	raw := ""
	if auth != nil {
		if auth.Attributes != nil {
			raw = strings.TrimSpace(auth.Attributes["base_url"])
		}
		if raw == "" {
			raw = strings.TrimSpace(claudeauth.ReadMetadataString(&auth.Metadata, "base_url"))
		}
	}
	if raw == "" {
		raw = "https://api.anthropic.com"
	}
	parsed, errParse := url.Parse(raw)
	if errParse != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		sum := sha256.Sum256([]byte(raw))
		return fmt.Sprintf("invalid:%x", sum[:8])
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	port := parsed.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}

// fallbackRoundTripper uses provider-specific TLS fingerprints for protected
// HTTPS hosts and falls back to the standard transport for all other requests.
type fallbackRoundTripper struct {
	anthropic http.RoundTripper
	chrome    http.RoundTripper
	fallback  http.RoundTripper
}

func (f *fallbackRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if IsAnthropicUpstreamURL(req.URL) {
		return f.anthropic.RoundTrip(req)
	}
	if req.URL.Scheme == "https" && strings.EqualFold(req.URL.Hostname(), "chatgpt.com") {
		return f.chrome.RoundTrip(req)
	}
	return f.fallback.RoundTrip(req)
}

// NewUtlsHTTPClient creates an HTTP client using provider-specific TLS
// fingerprints for protected hosts. It uses Claude Code's Node/OpenSSL profile
// for Anthropic and a Chrome profile for ChatGPT, with a standard-transport
// fallback for other hosts.
func NewUtlsHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	var ctxRoundTripper http.RoundTripper
	if ctx != nil {
		ctxRoundTripper, _ = ctx.Value("cliproxy.roundtripper").(http.RoundTripper)
	}

	bindIP, localIP, errBindIP := claudeBindIP(auth)
	if errBindIP != nil {
		client := &http.Client{Transport: claudeTransportError{err: errBindIP}}
		if timeout > 0 {
			client.Timeout = timeout
		}
		return client
	}
	owner := claudeTransportOwner(auth)
	origin := claudeTransportOrigin(auth)
	var chromeRT http.RoundTripper = newUtlsRoundTripper(proxyURL)
	var anthropicRT http.RoundTripper = cachedClaudeCodeRoundTripper(owner, proxyURL, bindIP, origin, localIP)
	var standardTransport http.RoundTripper = cachedClaudeStandardRoundTripper(owner, proxyURL, bindIP, origin, localIP)
	if proxyURL == "" && ctxRoundTripper != nil && bindIP == "" {
		chromeRT = ctxRoundTripper
		anthropicRT = ctxRoundTripper
		standardTransport = ctxRoundTripper
	}

	client := &http.Client{
		Transport: &fallbackRoundTripper{
			anthropic: anthropicRT,
			chrome:    chromeRT,
			fallback:  standardTransport,
		},
	}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}
