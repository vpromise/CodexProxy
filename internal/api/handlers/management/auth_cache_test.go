package management

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestManagementAuthCacheInvalidatesOnLegacyYAMLUpdate(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	yamlFor := func(secret string) string {
		hash, err := bcrypt.GenerateFromPassword([]byte(secret), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("host: 127.0.0.1\nport: 8317\nauth-dir: %q\napi-keys: [inference-key]\nremote-management:\n  secret-key: %q\n", filepath.Join(dir, "auths"), hash)
	}
	if err := os.WriteFile(path, []byte(yamlFor("key-one")), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(cfg, path, nil)
	router := gin.New()
	management := router.Group("/v0/management", h.Middleware())
	management.GET("/debug", h.GetDebug)
	management.PUT("/config.yaml", h.PutConfigYAML)
	request := func(method, endpoint, key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, endpoint, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:12345"
		req.Header.Set("Authorization", "Bearer "+key)
		r := httptest.NewRecorder()
		router.ServeHTTP(r, req)
		return r
	}
	for range 3 {
		if r := request("GET", "/v0/management/debug", "key-one", ""); r.Code != 200 {
			t.Fatalf("warm authentication: %d %s", r.Code, r.Body)
		}
	}
	if len(h.authCache.entries) != 1 {
		t.Fatal("successful authentication not cached")
	}
	if r := request("PUT", "/v0/management/config.yaml", "key-one", yamlFor("key-two")); r.Code != 200 {
		t.Fatalf("YAML rotation: %d %s", r.Code, r.Body)
	}
	if len(h.authCache.entries) != 0 {
		t.Fatal("YAML rotation retained verification cache")
	}
	if r := request("GET", "/v0/management/debug", "key-one", ""); r.Code != 401 {
		t.Fatalf("old key after rotation: %d", r.Code)
	}
	if r := request("GET", "/v0/management/debug", "key-two", ""); r.Code != 200 {
		t.Fatalf("new key after rotation: %d %s", r.Code, r.Body)
	}
	if cfg, err = config.LoadConfig(path); err != nil || bcrypt.CompareHashAndPassword([]byte(cfg.RemoteManagement.SecretKey), []byte("key-two")) != nil {
		t.Fatal("new secret not persisted")
	}
}

func TestManagementAuthCacheInvalidationDuringVerificationDoesNotRepopulate(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var comparisons atomic.Int32
	cache := managementAuthCache{bcryptCompare: func(_, _ []byte) error {
		if comparisons.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}}
	done := make(chan bool)
	go func() { done <- cache.verify("old-hash", "old-secret") }()
	<-started
	cache.invalidate()
	close(release)
	if !<-done {
		t.Fatal("already-started verification changed its original decision")
	}
	if len(cache.entries) != 0 {
		t.Fatal("invalidated verification repopulated cache")
	}
	if !cache.verify("old-hash", "old-secret") || comparisons.Load() != 2 {
		t.Fatal("old verification was reused after invalidation")
	}
}

func TestAuthenticateManagementKey_ReusesVerifiedSecretWithinOneHourTTL(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.DefaultCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	var compareCalls atomic.Int64
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			nowFunc: func() time.Time {
				nowMu.Lock()
				defer nowMu.Unlock()
				return now
			},
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("initial auth failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("initial bcrypt calls = %d, want 1", got)
	}

	for i := 0; i < 10; i++ {
		if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
			t.Fatalf("cached auth %d failed: status=%d msg=%q", i+1, statusCode, errMsg)
		}
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls after repeated polling = %d, want 1", got)
	}

	nowMu.Lock()
	now = now.Add(time.Hour - time.Second)
	nowMu.Unlock()
	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("auth just before 1h expiry failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls before 1h expiry = %d, want 1", got)
	}

	nowMu.Lock()
	now = now.Add(time.Second)
	nowMu.Unlock()
	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("auth at 1h expiry failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 2 {
		t.Fatalf("bcrypt calls at 1h expiry = %d, want 2", got)
	}

	if allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatalf("auth after re-verification failed: status=%d msg=%q", statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 2 {
		t.Fatalf("bcrypt calls after refresh = %d, want 2", got)
	}
}

func TestAuthenticateManagementKey_NeverCachesFailuresAndPreservesIPBan(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	var compareCalls atomic.Int64
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatal("expected valid secret to succeed")
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls = %d, want 1", got)
	}

	// Four wrong attempts must each invoke bcrypt (never negative-cached) and not ban yet.
	for i := 0; i < 4; i++ {
		allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed || statusCode != http.StatusUnauthorized || errMsg != "invalid management key" {
			t.Fatalf("wrong attempt %d = (%v, %d, %q), want (false, 401, invalid management key)", i+1, allowed, statusCode, errMsg)
		}
	}
	if got := compareCalls.Load(); got != 5 {
		t.Fatalf("bcrypt calls after 4 failures = %d, want 5", got)
	}

	// A cached hit must reset the failed-attempt counter.
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatal("expected cached valid secret to succeed and reset attempts")
	}
	if got := compareCalls.Load(); got != 5 {
		t.Fatalf("bcrypt calls on cached hit = %d, want 5", got)
	}

	// Five consecutive failures after reset trigger the 30-minute IP ban.
	for i := 0; i < 5; i++ {
		allowed, statusCode, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong-secret")
		if allowed || statusCode != http.StatusUnauthorized {
			t.Fatalf("post-reset failure %d = (%v, %d), want (false, 401)", i+1, allowed, statusCode)
		}
	}
	if got := compareCalls.Load(); got != 10 {
		t.Fatalf("bcrypt calls after 5 failures = %d, want 10", got)
	}

	// Even with the valid secret already cached, the banned IP must still receive 403.
	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed || statusCode != http.StatusForbidden || !strings.HasPrefix(errMsg, "IP banned due to too many failed attempts. Try again in") {
		t.Fatalf("banned auth with cached key = (%v, %d, %q), want (false, 403, IP banned...)", allowed, statusCode, errMsg)
	}
	if got := compareCalls.Load(); got != 10 {
		t.Fatalf("banned request should short-circuit before bcrypt; calls = %d, want 10", got)
	}
}

func TestAuthenticateManagementKey_InvalidatesCacheOnSecretChangeAndRemoval(t *testing.T) {
	hash1, errHash1 := bcrypt.GenerateFromPassword([]byte("secret-one"), bcrypt.MinCost)
	if errHash1 != nil {
		t.Fatalf("bcrypt.GenerateFromPassword(1) error = %v", errHash1)
	}
	hash2, errHash2 := bcrypt.GenerateFromPassword([]byte("secret-two"), bcrypt.MinCost)
	if errHash2 != nil {
		t.Fatalf("bcrypt.GenerateFromPassword(2) error = %v", errHash2)
	}
	var compareCalls atomic.Int64
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash1),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); !allowed {
		t.Fatal("expected secret-one to succeed")
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); !allowed {
		t.Fatal("expected cached secret-one to succeed")
	}
	if got := compareCalls.Load(); got != 1 {
		t.Fatalf("bcrypt calls before rotation = %d, want 1", got)
	}

	// Rotate via SetConfig.
	h.SetConfig(&config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: string(hash2),
		},
	})
	if allowed, statusCode, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); allowed || statusCode != http.StatusUnauthorized {
		t.Fatalf("stale secret-one after SetConfig = (%v, %d), want (false, 401)", allowed, statusCode)
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-two"); !allowed {
		t.Fatal("expected secret-two to succeed after rotation")
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-two"); !allowed {
		t.Fatal("expected cached secret-two to succeed")
	}
	if got := compareCalls.Load(); got != 3 {
		t.Fatalf("bcrypt calls after rotation = %d, want 3", got)
	}

	// Rotating back to hash1 must not reuse pre-rotation cached entry.
	h.SetConfig(&config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: string(hash1),
		},
	})
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); !allowed {
		t.Fatal("expected secret-one to succeed after rotating back")
	}
	if got := compareCalls.Load(); got != 4 {
		t.Fatalf("bcrypt calls after rotating back = %d, want 4", got)
	}

	// Direct h.cfg swap (e.g. PutConfigYAML) still misses because digest includes secretHash.
	h.mu.Lock()
	h.cfg = &config.Config{
		RemoteManagement: config.RemoteManagement{
			SecretKey: string(hash2),
		},
	}
	h.mu.Unlock()
	if allowed, statusCode, _ := h.AuthenticateManagementKey("127.0.0.1", true, "secret-one"); allowed || statusCode != http.StatusUnauthorized {
		t.Fatalf("stale secret-one after direct cfg swap = (%v, %d), want (false, 401)", allowed, statusCode)
	}

	// Clearing secret-key denies further requests immediately.
	h.SetConfig(&config.Config{})
	allowed, statusCode, errMsg := h.AuthenticateManagementKey("127.0.0.1", true, "secret-two")
	if allowed || statusCode != http.StatusForbidden || errMsg != "remote management key not set" {
		t.Fatalf("auth after secret removal = (%v, %d, %q), want (false, 403, remote management key not set)", allowed, statusCode, errMsg)
	}
}

func TestAuthenticateManagementKey_CoalescesConcurrentColdCacheLookups(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	var compareCalls atomic.Int64
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
		authCache: managementAuthCache{
			bcryptCompare: func(hashedPassword, password []byte) error {
				compareCalls.Add(1)
				if string(password) == "test-secret" {
					select {
					case started <- struct{}{}:
					default:
					}
					<-release
				}
				return bcrypt.CompareHashAndPassword(hashedPassword, password)
			},
		},
	}

	const workers = 8
	var wg sync.WaitGroup
	results := make([]bool, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
			results[idx] = allowed
		}(i)
	}

	<-started
	// A different/wrong secret runs independently without blocking on or succeeding from the valid lookup.
	wrongAllowed, wrongStatus, _ := h.AuthenticateManagementKey("127.0.0.2", true, "wrong-secret")
	if wrongAllowed || wrongStatus != http.StatusUnauthorized {
		t.Fatalf("concurrent wrong secret = (%v, %d), want (false, 401)", wrongAllowed, wrongStatus)
	}

	close(release)
	wg.Wait()

	for i, allowed := range results {
		if !allowed {
			t.Fatalf("worker %d failed", i)
		}
	}
	// 1 coalesced lookup for "test-secret" + 1 lookup for "wrong-secret".
	if got := compareCalls.Load(); got != 2 {
		t.Fatalf("bcrypt calls = %d, want 2", got)
	}
}

func TestManagementAuthCache_BoundsEntries(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cache := managementAuthCache{
		nowFunc: func() time.Time { return now },
		bcryptCompare: func(_, _ []byte) error {
			return nil
		},
	}
	for i := 0; i < managementAuthCacheMaxEntries+16; i++ {
		now = now.Add(time.Second)
		if !cache.verify(fmt.Sprintf("hash-%d", i), "secret") {
			t.Fatalf("verify(%d) = false, want true", i)
		}
	}
	cache.mu.Lock()
	entryCount := len(cache.entries)
	cache.mu.Unlock()
	if entryCount > managementAuthCacheMaxEntries {
		t.Fatalf("cache entries = %d, want <= %d", entryCount, managementAuthCacheMaxEntries)
	}
}

func BenchmarkAuthenticateManagementKey_Cached(b *testing.B) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-secret"), bcrypt.DefaultCost)
	if errHash != nil {
		b.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	h := &Handler{
		cfg: &config.Config{
			RemoteManagement: config.RemoteManagement{
				SecretKey: string(hash),
			},
		},
		failedAttempts: make(map[string]*attemptInfo),
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		b.Fatal("warmup auth failed")
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
			b.Fatal("cached auth failed")
		}
	}
}
