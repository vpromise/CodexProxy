package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestClaudeThreadOwnerHonorsCurrentAuthorization(t *testing.T) {
	for _, mode := range []string{"execute", "stream"} {
		for _, change := range []string{"round-robin", "reload", "refresh", "disabled", "cooldown", "revoked", "reassigned-pool", "foreign-caller", "conflicting-pin", "replaced-account", "removed-and-reimported", "model-alias", "alias-policy-changed"} {
			t.Run(mode+"/"+change, func(t *testing.T) {
				const model = "claude-sonnet-4-5"
				key := "synthetic-thread-" + t.Name()
				cfg := &config.Config{}
				cfg.APIKeys = []string{key, key + "other"}
				manager := coreauth.NewManager(nil, &coreauth.RoundRobinSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				executor := NewClaudeExecutor(cfg)
				manager.RegisterExecutor(executor)
				accounts := make([]*coreauth.Auth, 0, 2)
				for i := 0; i < 2; i++ {
					token := fmt.Sprintf("sk-ant-oat-thread-test-%d", i)
					a := &coreauth.Auth{ID: fmt.Sprintf("%s-%d", t.Name(), i), Provider: "claude", Status: coreauth.StatusActive, Attributes: map[string]string{"auth_kind": "oauth", "cloak_mode": "always"}, Metadata: claudeOAuthTestMetadata()}
					a.Metadata["access_token"] = token
					a.Metadata["organization_uuid"] = fmt.Sprintf("thread-org-%d", i)
					a.Metadata["pool_account_id"] = fmt.Sprintf("%s-account-%d", t.Name(), i)
					a.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
					a.Metadata[claudeAccountProfileTokenSHA256Key] = claudeAccountProfileTokenSHA256(token)
					saved, err := manager.Register(t.Context(), a)
					if err != nil {
						t.Fatal(err)
					}
					accounts = append(accounts, saved)
					registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: model, Type: "claude", Object: "model", OwnedBy: "anthropic"}, {ID: "thread-alias", Type: "claude", Object: "model", OwnedBy: "anthropic"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				}
				members := []string{coreauth.AccountIdentity(accounts[0]), coreauth.AccountIdentity(accounts[1])}
				cfg.AccessIsolation = config.AccessIsolation{Enabled: true, Pools: []config.AccountPool{{ID: "pool", Provider: "claude", Accounts: members}, {ID: "other-pool", Provider: "claude", Accounts: members}}, Keys: []config.ClientKeyBinding{{Key: key, Pool: "pool"}, {Key: key + "other", Pool: "pool"}}}
				manager.SetConfig(cfg)
				calls := []string{}
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls = append(calls, req.Header.Get("Authorization"))
					id := fmt.Sprintf("msg_%s_%d", strings.ReplaceAll(t.Name(), "/", "_"), len(calls))
					response := fmt.Sprintf(`{"type":"message","id":%q,"role":"assistant","model":%q,"content":[],"stop_reason":"end_turn"}`, id, model)
					if mode == "stream" {
						response = fmt.Sprintf("data: {\"type\":\"message_start\",\"message\":{\"id\":%q,\"type\":\"message\",\"role\":\"assistant\",\"model\":%q,\"content\":[]}}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", id, model)
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				})
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", http.RoundTripper(transport))
				ctx = coreauth.WithClientIdentity(ctx, key, "config-inline")
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
				invoke := func(payload string) error {
					request := coreexecutor.Request{Model: model, Payload: []byte(payload)}
					if change == "model-alias" {
						opts.Metadata = map[string]any{coreexecutor.RequestedModelMetadataKey: "thread-alias"}
					}
					if mode == "stream" {
						r, err := manager.ExecuteStream(ctx, []string{"claude"}, request, opts)
						if err != nil {
							return err
						}
						for c := range r.Chunks {
							if c.Err != nil {
								return c.Err
							}
						}
						return nil
					}
					_, err := manager.Execute(ctx, []string{"claude"}, request, opts)
					return err
				}
				create := `{"model":"claude-sonnet-4-5","max_tokens":1024,"thread":{"type":"create"},"messages":[{"role":"user","content":"hello"}]}`
				if err := invoke(create); err != nil {
					t.Fatal(err)
				}
				if len(calls) != 1 {
					t.Fatalf("create calls=%d", len(calls))
				}
				owner := accounts[0]
				sibling := accounts[1]
				if calls[0] != "Bearer "+accounts[0].Metadata["access_token"].(string) {
					owner, sibling = sibling, owner
				}
				owner, _ = manager.GetByID(owner.ID)
				expectedToken := calls[0]
				switch change {
				case "alias-policy-changed":
					owner.Attributes["cloak_mode"] = "never"
					if _, err := manager.Update(t.Context(), owner); err != nil {
						t.Fatal(err)
					}
				case "reload":
					manager.RegisterExecutor(executor.WithConfig(cfg))
				case "refresh":
					owner.Metadata["access_token"] = "sk-ant-oat-refreshed-thread-test"
					owner.Metadata[claudeAccountProfileTokenSHA256Key] = claudeAccountProfileTokenSHA256(owner.Metadata["access_token"].(string))
					expectedToken = "Bearer " + owner.Metadata["access_token"].(string)
					if _, err := manager.Update(t.Context(), owner); err != nil {
						t.Fatal(err)
					}
				case "disabled":
					owner.Disabled = true
					owner.Status = coreauth.StatusDisabled
					_, _ = manager.Update(t.Context(), owner)
				case "cooldown":
					manager.MarkResult(t.Context(), coreauth.Result{AuthID: owner.ID, Provider: "claude", Model: model, Success: false, Error: &coreauth.Error{HTTPStatus: 429, Message: "rate limited"}, RetryAfter: func() *time.Duration { d := time.Hour; return &d }()})
				case "revoked":
					cfg.AccessIsolation.Pools[0].Accounts = []string{coreauth.AccountIdentity(sibling)}
					manager.SetConfig(cfg)
				case "reassigned-pool":
					cfg.AccessIsolation.Keys[0].Pool = "other-pool"
					manager.SetConfig(cfg)
				case "foreign-caller":
					ctx = coreauth.WithClientIdentity(ctx, key+"other", "config-inline")
				case "conflicting-pin":
					opts.Metadata = map[string]any{coreexecutor.PinnedAuthMetadataKey: sibling.ID}
				case "replaced-account":
					owner.Metadata["organization_uuid"] = "different-organization"
					_, _ = manager.Update(t.Context(), owner)
					cfg.AccessIsolation.Pools[0].Accounts = append(members, coreauth.AccountIdentity(owner))
					manager.SetConfig(cfg)
				case "removed-and-reimported":
					manager.Remove(t.Context(), owner.ID)
					owner.RegistrationEpoch = 0
					owner.CredentialVersion = 0
					if _, err := manager.Register(t.Context(), owner); err != nil {
						t.Fatal(err)
					}
					registry.GetGlobalRegistry().RegisterClient(owner.ID, "claude", []*registry.ModelInfo{{ID: model, Type: "claude", Object: "model", OwnedBy: "anthropic"}})
				}
				previous := fmt.Sprintf("msg_%s_1", strings.ReplaceAll(t.Name(), "/", "_"))
				continued := fmt.Sprintf(`{"model":"claude-sonnet-4-5","max_tokens":1024,"thread":{"type":"continue","previous_message_id":%q},"messages":[{"role":"user","content":"again"}]}`, previous)
				err := invoke(continued)
				allowed := change == "round-robin" || change == "reload" || change == "refresh" || change == "model-alias"
				if allowed {
					if err != nil || len(calls) != 2 || calls[1] != expectedToken {
						t.Fatalf("continuation err=%v calls=%v", err, calls)
					}
				} else if err == nil || len(calls) != 1 {
					t.Fatalf("rejected continuation err=%v calls=%v", err, calls)
				}
			})
		}
	}
}
