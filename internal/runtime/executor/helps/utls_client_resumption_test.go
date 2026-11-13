package helps

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	gotls "crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

// newResumptionTestCertificate mints a short-lived self-signed leaf for the
// loopback TLS server used by the resumption test.
func newResumptionTestCertificate(t *testing.T) gotls.Certificate {
	t.Helper()
	key, errKey := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if errKey != nil {
		t.Fatalf("generate test key: %v", errKey)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "api.anthropic.com"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"api.anthropic.com"},
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, errCreate := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if errCreate != nil {
		t.Fatalf("create test certificate: %v", errCreate)
	}
	leaf, errParse := x509.ParseCertificate(der)
	if errParse != nil {
		t.Fatalf("parse test certificate: %v", errParse)
	}
	return gotls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
}

// TestClaudeCodeTLSSessionResumptionCompletesHandshake covers hybrid and
// classical key exchange, a cold HelloRetryRequest, TLS 1.2, and real session tickets.
func TestClaudeCodeTLSSessionResumptionCompletesHandshake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version uint16
		curve   gotls.CurveID
		rounds  int
	}{
		{name: "TLS13_MLKEM", version: gotls.VersionTLS13, curve: gotls.X25519MLKEM768, rounds: 2},
		{name: "TLS13_X25519", version: gotls.VersionTLS13, curve: gotls.X25519, rounds: 2},
		// uTLS v1.8.2 cannot recompute PSK binders after HelloRetryRequest.
		// Exercise the supported cold handshake here. The preexisting warm
		// handshake failure is recorded separately in the sync ledger.
		{name: "TLS13_P256_ColdHelloRetryRequest", version: gotls.VersionTLS13, curve: gotls.CurveP256, rounds: 1},
		{name: "TLS12_X25519", version: gotls.VersionTLS12, curve: gotls.X25519, rounds: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			certificate := newResumptionTestCertificate(t)
			roots := x509.NewCertPool()
			roots.AddCert(certificate.Leaf)
			listener, errListen := net.Listen("tcp", "127.0.0.1:0")
			if errListen != nil {
				t.Fatal(errListen)
			}
			serverDone := make(chan struct{})
			serverResults := make(chan error, tc.rounds)
			t.Cleanup(func() {
				if errClose := listener.Close(); errClose != nil && !errors.Is(errClose, net.ErrClosed) {
					t.Errorf("close listener: %v", errClose)
				}
				<-serverDone
			})
			serverConfig := &gotls.Config{
				Certificates:     []gotls.Certificate{certificate},
				MinVersion:       tc.version,
				MaxVersion:       tc.version,
				CurvePreferences: []gotls.CurveID{tc.curve},
			}
			go func() {
				defer close(serverDone)
				for round := 0; round < tc.rounds; round++ {
					raw, errAccept := listener.Accept()
					if errAccept != nil {
						serverResults <- errAccept
						return
					}
					// A test-only deadline bounds failed handshakes and cleanup.
					errServer := raw.SetDeadline(time.Now().Add(5 * time.Second))
					server := gotls.Server(raw, serverConfig)
					if errServer == nil {
						errServer = server.Handshake()
					}
					if errServer == nil {
						// Reading this greeting also consumes the session tickets.
						_, errServer = server.Write([]byte("ok\n"))
					}
					if errServer == nil {
						_, errServer = io.ReadFull(server, make([]byte, 4))
					}
					serverResults <- errors.Join(errServer, raw.Close())
				}
			}()

			sessionCache := tls.NewLRUClientSessionCache(claudeCodeSessionCacheCapacity)
			for round := 0; round < tc.rounds; round++ {
				func() {
					raw, errDial := net.Dial("tcp", listener.Addr().String())
					if errDial != nil {
						t.Fatal(errDial)
					}
					defer func() {
						if errClose := raw.Close(); errClose != nil {
							t.Errorf("close client: %v", errClose)
						}
					}()
					if errDeadline := raw.SetDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
						t.Fatal(errDeadline)
					}
					config := newClaudeCodeTLSConfig("api.anthropic.com", sessionCache)
					config.RootCAs = roots
					conn := tls.UClient(raw, config, tls.HelloCustom)
					if errPreset := conn.ApplyPreset(claudeCodeTLSClientHelloSpec()); errPreset != nil {
						t.Fatal(errPreset)
					}
					if errHandshake := conn.HandshakeContext(t.Context()); errHandshake != nil {
						t.Fatalf("round %d handshake: %v", round, errHandshake)
					}
					if _, errRead := io.ReadFull(conn, make([]byte, 3)); errRead != nil {
						t.Fatal(errRead)
					}
					if _, errWrite := conn.Write([]byte("bye\n")); errWrite != nil {
						t.Fatal(errWrite)
					}
					if errServer := <-serverResults; errServer != nil {
						t.Fatalf("round %d server: %v", round, errServer)
					}
					state := conn.ConnectionState()
					if state.Version != tc.version || state.DidResume != (round == 1) {
						t.Fatalf("round %d version=%x resumed=%v", round, state.Version, state.DidResume)
					}
					if round == 1 && tc.version == gotls.VersionTLS13 {
						hello := conn.HandshakeState.Hello.Raw
						record := append([]byte{22, 3, 1, byte(len(hello) >> 8), byte(len(hello))}, hello...)
						extensions := parseClientHelloExtensionLengths(t, record)
						if last := extensions[len(extensions)-1]; last[0] != 41 {
							t.Fatalf("last resumed extension = %v, want pre_shared_key (41)", last)
						}
					}
				}()
			}
		})
	}
}
