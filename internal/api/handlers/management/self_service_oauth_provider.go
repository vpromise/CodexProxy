package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func selfServiceStart(cfg *config.Config, r *selfServiceRecord) error {
	var err error
	r.State, err = selfServiceRandom(32)
	if err != nil {
		return err
	}
	switch r.Provider {
	case "claude":
		pkce, err := claude.GeneratePKCECodes()
		if err != nil {
			return err
		}
		r.Verifier = pkce.CodeVerifier
		r.Challenge = pkce.CodeChallenge
		r.AuthURL, r.State, err = claude.NewClaudeAuth(cfg).GenerateAuthURL(r.State, pkce)
		return err
	case "codex":
		pkce, err := codex.GeneratePKCECodes()
		if err != nil {
			return err
		}
		r.Verifier = pkce.CodeVerifier
		r.Challenge = pkce.CodeChallenge
		r.AuthURL, err = newCodexOAuthService(cfg).GenerateAuthURL(r.State, pkce)
		return err
	}
	return errors.New("unsupported provider")
}

// Retain the provider's complete credential representation, including identity,
// device IDs, expiry and refresh material. Tokens never enter public responses.
func selfServiceExchange(ctx context.Context, cfg *config.Config, r *selfServiceRecord, code string) (map[string]any, error) {
	var storage any
	switch r.Provider {
	case "claude":
		service := claude.NewClaudeAuth(cfg)
		bundle, err := service.ExchangeCodeForTokens(ctx, code, r.State, &claude.PKCECodes{CodeVerifier: r.Verifier, CodeChallenge: r.Challenge})
		if err != nil {
			return nil, err
		}
		token := service.CreateTokenStorage(bundle)
		token.Type = "claude"
		storage = token
	case "codex":
		service := newCodexOAuthService(cfg)
		bundle, err := service.ExchangeCodeForTokens(ctx, code, &codex.PKCECodes{CodeVerifier: r.Verifier, CodeChallenge: r.Challenge})
		if err != nil {
			return nil, err
		}
		token := service.CreateTokenStorage(bundle)
		token.Type = "codex"
		storage = token
	default:
		return nil, errors.New("unsupported provider")
	}
	data, err := json.Marshal(storage)
	if err != nil {
		return nil, err
	}
	var metadata map[string]any
	err = json.Unmarshal(data, &metadata)
	return metadata, err
}

func selfServiceCallback(raw string, r *selfServiceRecord) (string, error) {
	raw = strings.TrimSpace(raw)
	code, state := "", ""
	if strings.Contains(raw, "://") {
		parsed, err := url.Parse(raw)
		if err != nil {
			return "", errors.New("invalid callback")
		}
		// These fixed loopback redirects are the ones registered by the providers.
		expected := "localhost:1455"
		path := "/auth/callback"
		if r.Provider == "claude" {
			expected = "localhost:54545"
			path = "/callback"
		}
		if parsed.Scheme != "http" || parsed.Host != expected || parsed.Path != path || parsed.User != nil || parsed.Fragment != "" {
			return "", errors.New("unexpected callback")
		}
		query := parsed.Query()
		if query.Get("error") != "" || len(query["code"]) != 1 || len(query["state"]) != 1 {
			return "", errors.New("invalid callback")
		}
		code, state = query.Get("code"), query.Get("state")
	} else {
		code, state, _ = strings.Cut(raw, "#")
	}
	if code == "" || len(code) > 4096 || strings.ContainsAny(code, "#\r\n\x00") || state != r.State {
		return "", errors.New("callback state mismatch")
	}
	return code, nil
}
