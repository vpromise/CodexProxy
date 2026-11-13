package api

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func testServerTLSConfig(t *testing.T) config.TLSConfig {
	t.Helper()
	fixture := httptest.NewTLSServer(http.NotFoundHandler())
	certificate := fixture.TLS.Certificates[0]
	fixture.Close()
	key, errMarshal := x509.MarshalPKCS8PrivateKey(certificate.PrivateKey)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	if errWrite := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Certificate[0]}), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errWrite := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	return config.TLSConfig{Enable: true, Cert: certPath, Key: keyPath}
}

func TestServerStopBeforeStart(t *testing.T) {
	for _, tlsEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls=%t", tlsEnabled), func(t *testing.T) {
			cfg := &config.Config{}
			if tlsEnabled {
				cfg.TLS = testServerTLSConfig(t)
			}
			server := &Server{server: &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler()}, cfg: cfg}
			if errStop := server.Stop(context.Background()); errStop != nil {
				t.Fatal(errStop)
			}
			if errStart := server.Start(); errStart != nil {
				t.Fatal(errStart)
			}
			if server.muxBaseListener != nil || server.muxHTTPListener != nil || server.server.TLSConfig != nil {
				t.Fatal("Start initialized a listener or TLS after Stop completed")
			}
		})
	}
}

func TestServerStopConcurrentWithStart(t *testing.T) {
	for _, tlsEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("tls=%t", tlsEnabled), func(t *testing.T) {
			cfg := &config.Config{}
			if tlsEnabled {
				cfg.TLS = testServerTLSConfig(t)
			}
			for iteration := 0; iteration < 10; iteration++ {
				listener, errListen := net.Listen("tcp", "127.0.0.1:0")
				if errListen != nil {
					t.Fatal(errListen)
				}
				address := listener.Addr().String()
				if errClose := listener.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				server := &Server{server: &http.Server{Addr: address, Handler: http.NotFoundHandler()}, cfg: cfg}
				done := make(chan error, 1)
				go func() { done <- server.Start() }()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				for {
					connection, errDial := (&net.Dialer{}).DialContext(ctx, "tcp", address)
					if errDial == nil {
						if errClose := connection.Close(); errClose != nil {
							t.Fatal(errClose)
						}
						break
					}
					select {
					case errStart := <-done:
						t.Fatalf("Start exited before listening: %v", errStart)
					case <-ctx.Done():
						t.Fatal("Start did not open its listener")
					default:
					}
				}
				if errStop := server.Stop(ctx); errStop != nil {
					t.Fatal(errStop)
				}
				select {
				case errStart := <-done:
					if errStart != nil {
						t.Fatal(errStart)
					}
				case <-ctx.Done():
					t.Fatal("Start did not return after Stop")
				}
				cancel()
				listener, errListen = net.Listen("tcp", address)
				if errListen != nil {
					t.Fatalf("listener remained open after Stop: %v", errListen)
				}
				if errClose := listener.Close(); errClose != nil {
					t.Fatal(errClose)
				}
			}
		})
	}
}

func TestServerTLSInitializationFailureReleasesListener(t *testing.T) {
	for _, invalidPaths := range []bool{false, true} {
		t.Run(fmt.Sprintf("invalid_paths=%t", invalidPaths), func(t *testing.T) {
			listener, errListen := net.Listen("tcp", "127.0.0.1:0")
			if errListen != nil {
				t.Fatal(errListen)
			}
			address := listener.Addr().String()
			if errClose := listener.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			cfg := &config.Config{TLS: config.TLSConfig{Enable: true}}
			if invalidPaths {
				cfg.TLS.Cert = filepath.Join(t.TempDir(), "missing-cert.pem")
				cfg.TLS.Key = filepath.Join(t.TempDir(), "missing-key.pem")
			}
			server := &Server{server: &http.Server{Addr: address}, cfg: cfg}
			if errStart := server.Start(); errStart == nil {
				t.Fatal("Start accepted invalid TLS configuration")
			}
			for attempt := 0; attempt < 2; attempt++ {
				if errStop := server.Stop(context.Background()); errStop != nil {
					t.Fatal(errStop)
				}
			}
			listener, errListen = net.Listen("tcp", address)
			if errListen != nil {
				t.Fatalf("TLS initialization failure retained its listener: %v", errListen)
			}
			if errClose := listener.Close(); errClose != nil {
				t.Fatal(errClose)
			}
		})
	}
}
