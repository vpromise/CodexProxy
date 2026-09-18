package management

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestAPICallTokenPlaceholderValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		index      string
		header     string
		wantHeader string
		wantError  string
	}{
		{name: "missing index", header: "Bearer $TOKEN$", wantError: "auth token not found"},
		{name: "stale index", index: "missing", header: "Bearer $TOKEN$", wantError: "auth credential not found for auth_index"},
		{name: "empty token", index: "empty", header: "Bearer $TOKEN$", wantError: "auth token not found"},
		{name: "OAuth token", index: "oauth", header: "Bearer $TOKEN$", wantHeader: "Bearer test-oauth-token"},
		{name: "API key", index: "apikey", header: "Bearer $TOKEN$", wantHeader: "Bearer test-api-key"},
		{name: "repeated placeholder", index: "oauth", header: "$TOKEN$/$TOKEN$", wantHeader: "test-oauth-token/test-oauth-token"},
		{name: "manual header", header: "Bearer manual-token", wantHeader: "Bearer manual-token"},
		{name: "manual header with stale index", index: "missing", header: "Bearer manual-token", wantHeader: "Bearer manual-token"},
		{name: "body placeholder without header"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			const rawBody = `{"literal":"$TOKEN$"}`
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if got := r.Header.Get("Authorization"); got != tc.wantHeader {
					t.Errorf("upstream Authorization = %q, want %q", got, tc.wantHeader)
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil || string(body) != rawBody {
					t.Errorf("upstream body = %q, error = %v; want unchanged raw body", body, errRead)
				}
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte("accepted"))
			}))
			t.Cleanup(server.Close)

			manager := coreauth.NewManager(nil, nil, nil)
			for _, auth := range []*coreauth.Auth{
				{ID: "empty-token", Index: "empty", Provider: "claude"},
				{ID: "oauth-token", Index: "oauth", Provider: "claude", Metadata: map[string]any{"access_token": "test-oauth-token"}},
				{ID: "api-key", Index: "apikey", Provider: "codex", Attributes: map[string]string{"api_key": "test-api-key"}},
			} {
				if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
					t.Fatalf("register auth: %v", errRegister)
				}
			}
			h := &Handler{cfg: &config.Config{}, authManager: manager}
			router := gin.New()
			router.POST("/", h.APICall)
			body, errMarshal := json.Marshal(apiCallRequest{
				AuthIndexSnake: &tc.index,
				Method:         http.MethodPost,
				URL:            server.URL,
				Header:         map[string]string{"Authorization": tc.header},
				Data:           rawBody,
			})
			if errMarshal != nil {
				t.Fatalf("marshal request: %v", errMarshal)
			}
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)

			if tc.wantError != "" {
				if recorder.Code != http.StatusBadRequest || calls.Load() != 0 {
					t.Fatalf("status/calls = %d/%d, want 400/0; body = %s", recorder.Code, calls.Load(), recorder.Body.String())
				}
				var response struct {
					Error string `json:"error"`
				}
				if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil || response.Error != tc.wantError {
					t.Fatalf("error response = %s, decode error = %v; want %q", recorder.Body.String(), errDecode, tc.wantError)
				}
				return
			}
			if recorder.Code != http.StatusOK || calls.Load() != 1 {
				t.Fatalf("status/calls = %d/%d, want 200/1; body = %s", recorder.Code, calls.Load(), recorder.Body.String())
			}
			var response apiCallResponse
			if errDecode := json.Unmarshal(recorder.Body.Bytes(), &response); errDecode != nil {
				t.Fatalf("decode response: %v", errDecode)
			}
			if response.StatusCode != http.StatusAccepted || response.Body != "accepted" {
				t.Fatalf("upstream response = %+v", response)
			}
		})
	}
}

func TestAPICallUsesRequestProxyURL(t *testing.T) {
	t.Parallel()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("proxied"))
	}))
	defer proxyServer.Close()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:1"},
		},
	}
	router := gin.New()
	router.POST("/", h.APICall)

	body := `{"method":"GET","url":"http://upstream.invalid/test","proxy_url":"` + proxyServer.URL + `"}`
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status code = %d, want %d; body = %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response apiCallResponse
	if errDecode := json.NewDecoder(recorder.Body).Decode(&response); errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upstream status code = %d, want %d", response.StatusCode, http.StatusCreated)
	}
	if response.Body != "proxied" {
		t.Fatalf("upstream body = %q, want %q", response.Body, "proxied")
	}
}

func TestAPICallTransportDirectBypassesGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "direct"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected direct transport to disable proxy function")
	}
}

func TestAPICallTransportInvalidAuthFallsBackToGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}

	transport := h.apiCallTransport(&coreauth.Auth{ProxyURL: "bad-value"}, "")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://global-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://global-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportRequestProxyOverridesCredentialAndGlobalProxy(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, " http://request-proxy.example.com:8080 ")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}

	req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
	if errRequest != nil {
		t.Fatalf("http.NewRequest returned error: %v", errRequest)
	}

	proxyURL, errProxy := httpTransport.Proxy(req)
	if errProxy != nil {
		t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
	}
	if proxyURL == nil || proxyURL.String() != "http://request-proxy.example.com:8080" {
		t.Fatalf("proxy URL = %v, want http://request-proxy.example.com:8080", proxyURL)
	}
}

func TestAPICallTransportInvalidRequestProxyDoesNotFallBack(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
		},
	}
	auth := &coreauth.Auth{ProxyURL: "http://credential-proxy.example.com:8080"}

	transport := h.apiCallTransport(auth, "bad-value")
	httpTransport, ok := transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", transport)
	}
	if httpTransport.Proxy != nil {
		t.Fatal("expected invalid request proxy to avoid lower-priority proxy settings")
	}
}

func TestAPICallTransportAPIKeyAuthFallsBackToConfigProxyURL(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"},
			ClaudeKey: []config.ClaudeKey{{
				APIKey:   "claude-key",
				ProxyURL: "http://claude-proxy.example.com:8080",
			}},
			CodexKey: []config.CodexKey{{
				APIKey:   "codex-key",
				ProxyURL: "http://codex-proxy.example.com:8080",
			}},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "bohe",
				BaseURL: "https://bohe.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:   "compat-key",
					ProxyURL: "http://compat-proxy.example.com:8080",
				}},
			}},
		},
	}

	cases := []struct {
		name      string
		auth      *coreauth.Auth
		wantProxy string
	}{
		{
			name: "claude",
			auth: &coreauth.Auth{
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "claude-key"},
			},
			wantProxy: "http://claude-proxy.example.com:8080",
		},
		{
			name: "codex",
			auth: &coreauth.Auth{
				Provider:   "codex",
				Attributes: map[string]string{"api_key": "codex-key"},
			},
			wantProxy: "http://codex-proxy.example.com:8080",
		},
		{
			name: "openai-compatibility",
			auth: &coreauth.Auth{
				Provider: "bohe",
				Attributes: map[string]string{
					"api_key":      "compat-key",
					"compat_name":  "bohe",
					"provider_key": "bohe",
				},
			},
			wantProxy: "http://compat-proxy.example.com:8080",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			transport := h.apiCallTransport(tc.auth, "")
			httpTransport, ok := transport.(*http.Transport)
			if !ok {
				t.Fatalf("transport type = %T, want *http.Transport", transport)
			}

			req, errRequest := http.NewRequest(http.MethodGet, "https://example.com", nil)
			if errRequest != nil {
				t.Fatalf("http.NewRequest returned error: %v", errRequest)
			}

			proxyURL, errProxy := httpTransport.Proxy(req)
			if errProxy != nil {
				t.Fatalf("httpTransport.Proxy returned error: %v", errProxy)
			}
			if proxyURL == nil || proxyURL.String() != tc.wantProxy {
				t.Fatalf("proxy URL = %v, want %s", proxyURL, tc.wantProxy)
			}
		})
	}
}

func TestAuthByIndexDistinguishesSharedAPIKeysAcrossProviders(t *testing.T) {
	t.Parallel()

	manager := coreauth.NewManager(nil, nil, nil)
	claudeAuth := &coreauth.Auth{
		ID:       "claude:apikey:123",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key": "shared-key",
		},
	}
	compatAuth := &coreauth.Auth{
		ID:       "openai-compatibility:bohe:456",
		Provider: "bohe",
		Label:    "bohe",
		Attributes: map[string]string{
			"api_key":      "shared-key",
			"compat_name":  "bohe",
			"provider_key": "bohe",
		},
	}

	if _, errRegister := manager.Register(context.Background(), claudeAuth); errRegister != nil {
		t.Fatalf("register Claude auth: %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), compatAuth); errRegister != nil {
		t.Fatalf("register compat auth: %v", errRegister)
	}

	claudeIndex := claudeAuth.EnsureIndex()
	compatIndex := compatAuth.EnsureIndex()
	if claudeIndex == compatIndex {
		t.Fatalf("shared api key produced duplicate auth_index %q", claudeIndex)
	}

	h := &Handler{authManager: manager}

	gotClaude := h.authByIndex(claudeIndex)
	if gotClaude == nil {
		t.Fatal("expected Claude auth by index")
	}
	if gotClaude.ID != claudeAuth.ID {
		t.Fatalf("authByIndex(Claude) returned %q, want %q", gotClaude.ID, claudeAuth.ID)
	}

	gotCompat := h.authByIndex(compatIndex)
	if gotCompat == nil {
		t.Fatal("expected compat auth by index")
	}
	if gotCompat.ID != compatAuth.ID {
		t.Fatalf("authByIndex(compat) returned %q, want %q", gotCompat.ID, compatAuth.ID)
	}
}
