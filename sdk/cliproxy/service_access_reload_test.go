package cliproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

const accessReloadModel = "gpt-5.4"

// All provider traffic crosses this local proxy, which rejects destinations other
// than the synthetic upstream. CONNECT also covers the native WebSocket dialer.
func newAccessReloadProxy(t *testing.T, upstreamURL string) *httptest.Server {
	t.Helper()
	target, err := url.Parse(upstreamURL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	forward := httputil.NewSingleHostReverseProxy(target)
	forward.Transport = transport
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != target.Host {
			t.Errorf("blocked unexpected provider destination %q", r.Host)
			http.Error(w, "unexpected provider destination", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodConnect {
			forward.ServeHTTP(w, r)
			return
		}
		upstream, errDial := net.Dial("tcp", target.Host)
		if errDial != nil {
			http.Error(w, errDial.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = upstream.Close() }()
		client, buffered, errHijack := w.(http.Hijacker).Hijack()
		if errHijack != nil {
			t.Error(errHijack)
			return
		}
		defer func() { _ = client.Close() }()
		if _, errWrite := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
			return
		}
		if errFlush := buffered.Flush(); errFlush != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, buffered)
			_ = upstream.Close()
			close(done)
		}()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		<-done
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

type accessReloadService struct {
	url        string
	configPath string
	client     *http.Client
}

func newAccessReloadService(t *testing.T, upstreamURL string) *accessReloadService {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	proxy := newAccessReloadProxy(t, upstreamURL)
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	payload := fmt.Sprintf(`host: 127.0.0.1
port: %d
auth-dir: %q
commercial-mode: true
proxy-url: %q
api-keys: [synthetic-old-key]
remote-management:
  secret-key: synthetic-management-key
  disable-control-panel: true
plugins:
  enabled: false
  dir: %q
codex-api-key:
  - api-key: synthetic-upstream-key
    base-url: %q
    websockets: true
    models:
      - name: %s
`, port, filepath.Join(dir, "auths"), proxy.URL, filepath.Join(dir, "plugins"), upstreamURL, accessReloadModel)
	if errWrite := os.WriteFile(path, []byte(payload), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	previousStore := sdkAuth.GetTokenStore()
	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(cfg.AuthDir)
	sdkAuth.RegisterTokenStore(store)
	t.Cleanup(func() { sdkAuth.RegisterTokenStore(previousStore) })
	manager := coreauth.NewManager(store, nil, nil)
	t.Cleanup(func() {
		for _, account := range manager.List() {
			registry.GetGlobalRegistry().UnregisterClient(account.ID)
		}
	})
	ready := make(chan struct{})
	serverReady := make(chan struct{})
	var serverReadyOnce sync.Once
	transport := &http.Transport{Proxy: nil}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(path).
		WithCoreAuthManager(manager).
		WithServerOptions(api.WithEngineConfigurator(func(e *gin.Engine) {
			e.GET("/test-ready", func(c *gin.Context) {
				serverReadyOnce.Do(func() { close(serverReady) })
				c.Status(http.StatusNoContent)
			})
		})).
		WithWatcherFactory(func(configPath, authDir string, reload func(*config.Config)) (*WatcherWrapper, error) {
			// Wait for the real listener through a test-only readiness route. This
			// also orders startup before config reloads, unlike Run's startup sleep.
			deadline := time.Now().Add(5 * time.Second)
			for {
				resp, errReady := client.Get(baseURL + "/test-ready")
				if errReady == nil {
					_ = resp.Body.Close()
					<-serverReady
					break
				}
				if time.Now().After(deadline) {
					return nil, fmt.Errorf("wait for test listener: %w", errReady)
				}
				time.Sleep(10 * time.Millisecond)
			}
			w, errWatcher := defaultWatcherFactory(configPath, authDir, reload)
			if errWatcher != nil {
				return nil, errWatcher
			}
			start := w.start
			w.start = func(ctx context.Context) error {
				if errStart := start(ctx); errStart != nil {
					return errStart
				}
				close(ready)
				return nil
			}
			return w, nil
		}).Build()
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case errRun := <-done:
			if errRun != nil && !errors.Is(errRun, context.Canceled) {
				t.Error(errRun)
			}
		case <-time.After(10 * time.Second):
			t.Error("service did not stop")
		}
	})
	select {
	case <-ready:
	case errRun := <-done:
		done <- errRun
		t.Fatalf("service exited before watcher startup: %v", errRun)
	case <-time.After(10 * time.Second):
		t.Fatal("service did not start")
	}
	return &accessReloadService{url: baseURL, configPath: path, client: client}
}

func (s *accessReloadService) request(t *testing.T, method, path, key, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, s.url+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	payload, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	return resp.StatusCode, string(payload)
}

func (s *accessReloadService) waitForKey(t *testing.T, key string, status int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, body := s.request(t, http.MethodGet, "/v1/models", key, "")
		if got == status {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("key authorization returned %d, want %d: %s", got, status, body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAccessReloadPreservesActiveWebSocketGeneration(t *testing.T) {
	for _, workflow := range []string{"management", "file"} {
		t.Run(workflow, func(t *testing.T) { testAccessReloadWebSocket(t, workflow) })
	}
}

func (s *accessReloadService) rotateKey(t *testing.T, workflow string) {
	t.Helper()
	if workflow == "management" {
		status, body := s.request(t, http.MethodPut, "/v0/management/api-keys", "synthetic-management-key", `["synthetic-new-key"]`)
		if status != http.StatusOK {
			t.Fatalf("save access configuration: %d %s", status, body)
		}
	} else {
		cfg, errLoad := config.LoadConfig(s.configPath)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		cfg.APIKeys = []string{"synthetic-new-key"}
		if errSave := config.SaveConfigPreserveComments(s.configPath, cfg); errSave != nil {
			t.Fatal(errSave)
		}
	}
	s.waitForKey(t, "synthetic-old-key", http.StatusUnauthorized)
	s.waitForKey(t, "synthetic-new-key", http.StatusOK)
	status, body := s.request(t, http.MethodGet, "/v0/management/config.yaml", "synthetic-management-key", "")
	var persisted config.Config
	if errParse := yaml.Unmarshal([]byte(body), &persisted); status != http.StatusOK || errParse != nil {
		t.Fatalf("read persisted configuration: %d %s, %v", status, body, errParse)
	}
	if len(persisted.APIKeys) != 1 || persisted.APIKeys[0] != "synthetic-new-key" {
		t.Fatal("saved configuration does not contain the new client key")
	}
}

func testAccessReloadWebSocket(t *testing.T, workflow string) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseGeneration := func() { releaseOnce.Do(func() { close(release) }) }
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Error(errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Error(errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_mock","model":"gpt-5.4"}}`)); errWrite != nil {
			t.Error(errWrite)
			return
		}
		<-release
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_mock","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
		// Keep the upstream session alive until the downstream client closes it.
		_, _, _ = conn.ReadMessage()
	}))
	t.Cleanup(upstream.Close)
	s := newAccessReloadService(t, upstream.URL)
	t.Cleanup(releaseGeneration)
	conn, _, errDial := websocket.DefaultDialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(s.url, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer synthetic-old-key"}})
	if errDial != nil {
		t.Fatal(errDial)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"role":"user","content":"hello"}]}`)); errWrite != nil {
		t.Fatal(errWrite)
	}
	_, created, errRead := conn.ReadMessage()
	if errRead != nil || gjson.GetBytes(created, "type").String() != "response.created" {
		t.Fatalf("generation did not start: %s, %v", created, errRead)
	}
	s.rotateKey(t, workflow)
	releaseGeneration()
	_, completed, errComplete := conn.ReadMessage()
	if errComplete != nil || gjson.GetBytes(completed, "type").String() != "response.completed" {
		t.Fatalf("authorized generation interrupted by access reload: %s, %v", completed, errComplete)
	}
}

func TestAccessReloadPreservesActiveHTTPGeneration(t *testing.T) {
	for _, workflow := range []string{"management", "file"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", workflow, stream), func(t *testing.T) {
				started, release := make(chan struct{}, 1), make(chan struct{})
				var releaseOnce sync.Once
				releaseGeneration := func() { releaseOnce.Do(func() { close(release) }) }
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") != "Bearer synthetic-upstream-key" {
						t.Error("unexpected upstream credentials")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_mock\",\"model\":\"gpt-5.4\"}}\n\n")
					_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"output_index\":0,\"content_index\":0,\"delta\":\"synthetic answer\"}\n\n")
					w.(http.Flusher).Flush()
					started <- struct{}{}
					select {
					case <-release:
						_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_mock\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
					case <-r.Context().Done():
					}
				}))
				t.Cleanup(upstream.Close)
				s := newAccessReloadService(t, upstream.URL)
				t.Cleanup(releaseGeneration)
				payload := fmt.Sprintf(`{"model":"gpt-5.4","input":"hello","stream":%t}`, stream)
				type result struct {
					status int
					body   string
					err    error
				}
				generation := make(chan result, 1)
				visible := make(chan struct{})
				go func() {
					req, errRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, s.url+"/v1/responses", strings.NewReader(payload))
					if errRequest != nil {
						generation <- result{err: errRequest}
						return
					}
					req.Header.Set("Authorization", "Bearer synthetic-old-key")
					req.Header.Set("Content-Type", "application/json")
					resp, errDo := s.client.Do(req)
					if errDo != nil {
						generation <- result{err: errDo}
						return
					}
					defer func() { _ = resp.Body.Close() }()
					reader := bufio.NewReader(resp.Body)
					var first string
					if stream {
						first, _ = reader.ReadString('\n')
						close(visible)
					}
					body, errRead := io.ReadAll(reader)
					generation <- result{status: resp.StatusCode, body: first + string(body), err: errRead}
				}()
				select {
				case <-started:
				case failed := <-generation:
					t.Fatalf("generation did not reach local upstream: %+v", failed)
				case <-time.After(5 * time.Second):
					t.Fatal("generation did not reach local upstream")
				}
				if stream {
					select {
					case <-visible:
					case <-time.After(5 * time.Second):
						t.Fatal("SSE did not reach the client")
					}
				}
				s.rotateKey(t, workflow)
				releaseGeneration()
				select {
				case got := <-generation:
					if got.err != nil || got.status != http.StatusOK || !strings.Contains(got.body, `"status":"completed"`) {
						t.Fatalf("authorized generation interrupted: %+v", got)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("authorized generation did not finish")
				}
				status, body := s.request(t, http.MethodPost, "/v1/responses", "synthetic-new-key", payload)
				if status != http.StatusOK || !strings.Contains(body, `"status":"completed"`) {
					t.Fatalf("new key cannot generate a response: %d %s", status, body)
				}
			})
		}
	}
}

func TestAccessReloadStillAppliesProviderConfiguration(t *testing.T) {
	requests := make(chan []byte, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Error(errRead)
			return
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_mock\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(upstream.Close)
	s := newAccessReloadService(t, upstream.URL)
	generate := func(key string) []byte {
		t.Helper()
		status, body := s.request(t, http.MethodPost, "/v1/responses", key, `{"model":"gpt-5.4","input":"hello"}`)
		if status != http.StatusOK {
			t.Fatalf("generate: %d %s", status, body)
		}
		return <-requests
	}
	if body := generate("synthetic-old-key"); gjson.GetBytes(body, "metadata.reload").Exists() {
		t.Fatalf("unexpected initial override: %s", body)
	}
	cfg, errLoad := config.LoadConfig(s.configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	cfg.APIKeys = []string{"synthetic-provider-key"}
	cfg.Payload.Override = []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: accessReloadModel}},
		Params: map[string]any{"metadata.reload": "updated"},
	}}
	content, errMarshal := yaml.Marshal(cfg)
	if errMarshal != nil {
		t.Fatal(errMarshal)
	}
	status, body := s.request(t, http.MethodPut, "/v0/management/config.yaml", "synthetic-management-key", string(content))
	if status != http.StatusOK {
		t.Fatalf("save provider configuration: %d %s", status, body)
	}
	s.waitForKey(t, "synthetic-provider-key", http.StatusOK)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if body := generate("synthetic-provider-key"); gjson.GetBytes(body, "metadata.reload").String() == "updated" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider configuration was not applied")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// An invalid later save must neither replace the accepted configuration nor
	// discard its access policy when subsequent access-only edits are applied.
	status, _ = s.request(t, http.MethodPut, "/v0/management/config.yaml", "synthetic-management-key", "codex-api-key:\n  - api-key: synthetic\n    weight: 1000001\n")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("invalid configuration returned %d, want 422", status)
	}
	s.rotateKey(t, "management")
	s.waitForKey(t, "synthetic-provider-key", http.StatusUnauthorized)
	if body := generate("synthetic-new-key"); gjson.GetBytes(body, "metadata.reload").String() != "updated" {
		t.Fatalf("access-only update lost provider configuration: %s", body)
	}
}

func TestAccessIsolationHTTPAndCatalogs(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_mock\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	t.Cleanup(upstream.Close)
	s := newAccessReloadService(t, upstream.URL)
	status, body := s.request(t, http.MethodGet, "/v0/management/access-isolation", "synthetic-management-key", "")
	if status != 200 {
		t.Fatalf("inventory: %d %s", status, body)
	}
	id := gjson.Get(body, "accounts.0.id").String()
	if id == "" {
		t.Fatal("no native account identity")
	}
	policy := config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{id}}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-old-key", Pool: "one"}}}
	save := func() {
		t.Helper()
		_, view := s.request(t, http.MethodGet, "/v0/management/access-isolation", "synthetic-management-key", "")
		payload, _ := json.Marshal(map[string]any{"revision": gjson.Get(view, "revision").String(), "settings": policy})
		code, response := s.request(t, http.MethodPut, "/v0/management/access-isolation", "synthetic-management-key", string(payload))
		if code != 200 {
			t.Fatalf("save policy: %d %s", code, response)
		}
	}
	save()
	status, body = s.request(t, http.MethodGet, "/v1/models", "synthetic-old-key", "")
	if status != 200 || gjson.Get(body, "data.#").Int() != 1 {
		t.Fatalf("acknowledged scoped catalog: %d %s", status, body)
	}
	status, body = s.request(t, http.MethodPost, "/v1/responses", "synthetic-old-key", `{"model":"gpt-5.4","input":"hello"}`)
	if status != 200 {
		t.Fatalf("authorized request: %d %s", status, body)
	}
	policy.Pools[0].Accounts = nil
	save()
	status, body = s.request(t, http.MethodGet, "/v1/models", "synthetic-old-key", "")
	if status != 200 || gjson.Get(body, "data.#").Int() != 0 {
		t.Fatalf("acknowledged empty pool catalog: %d %s", status, body)
	}
	before := calls.Load()
	for _, path := range []string{"/v1/responses", "/v1/responses/compact", "/backend-api/codex/responses", "/v1/alpha/search"} {
		status, body = s.request(t, http.MethodPost, path, "synthetic-old-key", `{"model":"gpt-5.4","input":"hello"}`)
		if status < 400 {
			t.Fatalf("empty pool reached %s: %d %s", path, status, body)
		}
	}
	if calls.Load() != before {
		t.Fatal("empty pool contacted upstream")
	}
}

func (s *accessReloadService) saveIsolationPolicy(t *testing.T, policy config.AccessIsolation) {
	t.Helper()
	_, view := s.request(t, http.MethodGet, "/v0/management/access-isolation", "synthetic-management-key", "")
	payload, _ := json.Marshal(map[string]any{"revision": gjson.Get(view, "revision").String(), "settings": policy})
	code, body := s.request(t, http.MethodPut, "/v0/management/access-isolation", "synthetic-management-key", string(payload))
	if code != 200 {
		t.Fatalf("save policy: %d %s", code, body)
	}
}

func TestAccessIsolationWebSocketRechecksExistingConnection(t *testing.T) {
	for _, initiallyEnabled := range []bool{false, true} {
		t.Run(fmt.Sprintf("initially_enabled_%t", initiallyEnabled), func(t *testing.T) {
			var calls atomic.Int32
			release := make(chan struct{})
			var once sync.Once
			finish := func() { once.Do(func() { close(release) }) }
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					return
				}
				defer func() { _ = conn.Close() }()
				for {
					if _, _, err := conn.ReadMessage(); err != nil {
						return
					}
					calls.Add(1)
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"resp_mock","model":"gpt-5.4"}}`))
					<-release
					_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp_mock","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`))
				}
			}))
			t.Cleanup(upstream.Close)
			s := newAccessReloadService(t, upstream.URL)
			t.Cleanup(finish)
			policy := config.AccessIsolation{Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex"}}}
			if initiallyEnabled {
				_, view := s.request(t, http.MethodGet, "/v0/management/access-isolation", "synthetic-management-key", "")
				policy = config.AccessIsolation{Enabled: initiallyEnabled, Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{gjson.Get(view, "accounts.0.id").String()}}}, Keys: []config.ClientKeyBinding{{Key: "synthetic-old-key", Pool: "one"}}}
				s.saveIsolationPolicy(t, policy)
			}

			conn, _, err := websocket.DefaultDialer.DialContext(t.Context(), "ws"+strings.TrimPrefix(s.url, "http")+"/v1/responses", http.Header{"Authorization": {"Bearer synthetic-old-key"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			request := []byte(`{"type":"response.create","model":"gpt-5.4","input":[{"role":"user","content":"hello"}]}`)
			if err := conn.WriteMessage(websocket.TextMessage, request); err != nil {
				t.Fatal(err)
			}
			if _, body, err := conn.ReadMessage(); err != nil || gjson.GetBytes(body, "type").String() != "response.created" {
				t.Fatalf("start: %s %v", body, err)
			}
			policy.Enabled = true
			policy.Keys = nil
			s.saveIsolationPolicy(t, policy)
			s.waitForKey(t, "synthetic-old-key", http.StatusForbidden)
			finish()
			if _, body, err := conn.ReadMessage(); err != nil || gjson.GetBytes(body, "type").String() != "response.completed" {
				t.Fatalf("in-progress response interrupted: %s %v", body, err)
			}
			if err := conn.WriteMessage(websocket.TextMessage, request); err != nil {
				t.Fatal(err)
			}
			if _, body, err := conn.ReadMessage(); err != nil || !strings.Contains(string(body), "access_denied") {
				t.Fatalf("next message authorization: %s %v", body, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("unauthorized upstream calls: %d", calls.Load())
			}
		})
	}
}

func TestAccessIsolationOAuthMaintenance(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("maintenance contacted upstream")
		w.WriteHeader(502)
	}))
	t.Cleanup(upstream.Close)
	s := newAccessReloadService(t, upstream.URL)
	upload := func(name, subject, token string) {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"type": "codex", "account_id": subject, "access_token": token, "email": "synthetic@example.invalid"})
		status, body := s.request(t, http.MethodPost, "/v0/management/auth-files?name="+name, "synthetic-management-key", string(payload))
		if status != 200 {
			t.Fatalf("upload: %d %s", status, body)
		}
	}
	identity := func(label string) string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, body := s.request(t, http.MethodGet, "/v0/management/access-isolation", "synthetic-management-key", "")
			for _, a := range gjson.Get(body, "accounts").Array() {
				if a.Get("label").String() == label {
					return a.Get("id").String()
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("missing identity %s: %s", label, body)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	upload("synthetic-oauth.json", "synthetic-account", "synthetic-token-one")
	id := identity("synthetic@example.invalid")
	policy := config.AccessIsolation{Pools: []config.AccountPool{{ID: "one", Name: "One", Provider: "codex", Accounts: []string{id}}}}
	s.saveIsolationPolicy(t, policy)
	upload("synthetic-oauth.json", "synthetic-account", "synthetic-token-two")
	if got := identity("synthetic@example.invalid"); got != id {
		t.Fatal("same account lost identity")
	}
	cfg, err := config.LoadConfig(s.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(cfg.AuthDir, "synthetic-oauth.json"), filepath.Join(cfg.AuthDir, "renamed-oauth.json")); err != nil {
		t.Fatal(err)
	}
	// The public file inventory observes the watcher applying the rename.
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body := s.request(t, http.MethodGet, "/v0/management/auth-files", "synthetic-management-key", "")
		if strings.Contains(body, "renamed-oauth.json") && !strings.Contains(body, "synthetic-oauth.json") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rename did not settle: %s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := identity("synthetic@example.invalid"); got != id {
		t.Fatal("rename lost identity")
	}
	status, body := s.request(t, http.MethodDelete, "/v0/management/auth-files?name=renamed-oauth.json", "synthetic-management-key", "")
	if status != 200 {
		t.Fatalf("delete: %d %s", status, body)
	}
	upload("renamed-oauth.json", "synthetic-account", "synthetic-token-three")
	if got := identity("synthetic@example.invalid"); got == id {
		t.Fatal("reimport reused a deleted record identity")
	}
	_, body = s.request(t, http.MethodGet, "/v0/management/access-isolation", "synthetic-management-key", "")
	if gjson.Get(body, "settings.pools.0.accounts.#").Int() != 0 {
		t.Fatalf("reimport recovered deleted membership: %s", body)
	}
}
