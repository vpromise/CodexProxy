package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

type hostHTTPClient struct {
	host     *Host
	auth     *coreauth.Auth
	provider string
}

func (h *Host) newHTTPClient(auth *coreauth.Auth, providers ...string) pluginapi.HostHTTPClient {
	provider := ""
	if len(providers) > 0 {
		provider = providers[0]
	}
	return &hostHTTPClient{host: h, auth: auth, provider: provider}
}

func (c *hostHTTPClient) Do(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, cfg, errDo := c.doHTTP(ctx, req)
	if errDo != nil {
		return pluginapi.HTTPResponse{}, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("pluginhost: response body close error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	body, errReadAll := io.ReadAll(resp.Body)
	if len(body) > 0 {
		helps.AppendAPIResponseChunk(ctx, cfg, body)
	}
	if errReadAll != nil {
		helps.RecordAPIResponseError(ctx, cfg, errReadAll)
		return pluginapi.HTTPResponse{}, fmt.Errorf("read host http response: %w", errReadAll)
	}
	return pluginapi.HTTPResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeader(resp.Header),
		Body:       body,
	}, nil
}

func (c *hostHTTPClient) DoStream(ctx context.Context, req pluginapi.HTTPRequest) (pluginapi.HTTPStreamResponse, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	resp, cfg, errDo := c.doHTTP(ctx, req)
	if errDo != nil {
		return pluginapi.HTTPStreamResponse{}, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, cfg, resp.StatusCode, resp.Header.Clone())
	chunks := make(chan pluginapi.HTTPStreamChunk)
	go func() {
		defer close(chunks)
		defer func() {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Warnf("pluginhost: stream response body close error: %v", errClose)
			}
		}()
		buf := make([]byte, 32*1024)
		for {
			n, errRead := resp.Body.Read(buf)
			if n > 0 {
				payload := bytes.Clone(buf[:n])
				helps.AppendAPIResponseChunk(ctx, cfg, payload)
				select {
				case <-ctx.Done():
					return
				case chunks <- pluginapi.HTTPStreamChunk{Payload: payload}:
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, cfg, errRead)
					select {
					case <-ctx.Done():
					case chunks <- pluginapi.HTTPStreamChunk{Err: errRead}:
					}
				}
				return
			}
		}
	}()
	return pluginapi.HTTPStreamResponse{
		StatusCode: resp.StatusCode,
		Headers:    cloneHeader(resp.Header),
		Chunks:     chunks,
	}, nil
}

func (c *hostHTTPClient) doHTTP(ctx context.Context, req pluginapi.HTTPRequest) (*http.Response, *config.Config, error) {
	if c == nil || c.host == nil {
		return nil, nil, fmt.Errorf("host http client is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg := c.host.currentRuntimeConfig()
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	httpReq, errNewRequest := http.NewRequestWithContext(ctx, method, req.URL, bytes.NewReader(bytes.Clone(req.Body)))
	if errNewRequest != nil {
		return nil, cfg, fmt.Errorf("create host http request: %w", errNewRequest)
	}
	httpReq.Header = cloneHeader(req.Headers)
	c.recordHTTPRequest(ctx, cfg, httpReq, req.Body)
	client := helps.NewProxyAwareHTTPClient(ctx, cfg, c.auth, 0)
	if client == nil {
		client = &http.Client{}
	}
	installPluginRedirectPolicy(client)
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, cfg, errDo)
		return nil, cfg, fmt.Errorf("execute host http request: %w", errDo)
	}
	return resp, cfg, nil
}

func (c *hostHTTPClient) recordHTTPRequest(ctx context.Context, cfg *config.Config, req *http.Request, body []byte) {
	if req == nil {
		return
	}
	provider := c.provider
	var authID, authLabel, authType, authValue string
	if c.auth != nil {
		authID = c.auth.ID
		authLabel = c.auth.Label
		authType, authValue = c.auth.AccountInfo()
		if provider == "" {
			provider = c.auth.Provider
		}
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       req.URL.String(),
		Method:    req.Method,
		Headers:   req.Header.Clone(),
		Body:      bytes.Clone(body),
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func (h *Host) currentRuntimeConfig() *config.Config {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.runtimeConfig
}

func installPluginRedirectPolicy(client *http.Client) {
	if client == nil {
		return
	}
	previous := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return checkPluginRedirect(req, via, previous)
	}
}

func checkPluginRedirect(req *http.Request, via []*http.Request, previous func(*http.Request, []*http.Request) error) error {
	// net/http rebuilds each redirect request from the initial request's headers,
	// so strip credential headers whenever the target or any intermediate hop
	// leaves the initial request's origin (scheme + host:port).
	if crossOriginRedirect(req, via) {
		for name := range req.Header {
			if isPluginCredentialHeader(name) {
				// Delete the exact map key because Header.Del canonicalizes and
				// would miss non-canonical keys such as "x-api-key".
				delete(req.Header, name)
			}
		}
	}
	if previous != nil {
		return previous(req, via)
	}
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return nil
}

func crossOriginRedirect(req *http.Request, via []*http.Request) bool {
	if req == nil || len(via) == 0 || via[0] == nil {
		return false
	}
	origin := via[0].URL
	if !sameRedirectOrigin(origin, req.URL) {
		return true
	}
	for _, hop := range via[1:] {
		if hop == nil || !sameRedirectOrigin(origin, hop.URL) {
			return true
		}
	}
	return false
}

func sameRedirectOrigin(a, b *url.URL) bool {
	if a == nil || b == nil || a.Hostname() == "" || b.Hostname() == "" {
		return false
	}
	return strings.EqualFold(a.Scheme, b.Scheme) &&
		strings.EqualFold(a.Hostname(), b.Hostname()) &&
		redirectOriginPort(a) == redirectOriginPort(b)
}

func redirectOriginPort(u *url.URL) string {
	if u == nil {
		return ""
	}
	if port := u.Port(); port != "" {
		return port
	}
	switch strings.ToLower(u.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func isPluginCredentialHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	switch lower {
	case "authorization", "proxy-authorization", "cookie", "cookie2", "www-authenticate":
		return true
	}
	return strings.Contains(lower, "api-key") ||
		strings.Contains(lower, "apikey") ||
		strings.Contains(lower, "api_key") ||
		strings.Contains(lower, "token") ||
		strings.Contains(lower, "secret") ||
		strings.Contains(lower, "credential")
}
