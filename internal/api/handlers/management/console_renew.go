package management

import (
	"context"
	"errors"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// renew changes only the original owner's credential material. It never enables
// the account, changes its pool identity, transfers owner or replays an old token.
func (n *consoleNode) renew(ctx context.Context, r *selfServiceRecord) error {
	root, err := n.read(r.RenewalOf)
	if err != nil || root.Status != "accepted" || root.RenewalOf != "" {
		return errors.New("original owner unavailable")
	}
	if r.Status == "accepted" {
		if n.findAuth(r) == nil {
			return errors.New("account changed")
		}
		return nil
	}
	if r.Status != "pending" {
		return errors.New("pending renewal required")
	}
	if _, err = n.h.selfServiceRecordConfig(r); err != nil {
		return err
	}
	h := n.h
	selfServiceMu.Lock()
	defer selfServiceMu.Unlock()
	h.authStatusMu.Lock()
	defer h.authStatusMu.Unlock()
	current, err := selfServiceRead(n.authDir+".self-service-oauth", r.ID)
	if err != nil || current.Status != "pending" {
		return errors.New("renewal changed")
	}
	a := n.findAuth(root)
	if a == nil {
		return errors.New("account changed")
	}
	binding, err := coreauth.ConsoleCredentialBinding(a)
	if err != nil || !reflect.DeepEqual(binding, r.Console) {
		return errors.New("owner changed")
	}
	if a.Metadata["console_renewal_id"] != r.ID {
		if !a.Disabled || n.account(a).Revision != r.RenewalRevision {
			return errors.New("account changed")
		}
		expiry, _ := current.Credentials["expired"].(string)
		tm, e := time.Parse(time.RFC3339, expiry)
		if e != nil || !tm.After(time.Now()) {
			return errors.New("needs reauthorization")
		}
		incoming := &coreauth.Auth{Provider: r.Provider, Metadata: current.Credentials}
		if consolePrincipal(current.Credentials, r.Provider) == "" || consolePrincipal(current.Credentials, r.Provider) != consolePrincipal(a.Metadata, a.Provider) || coreauth.AccountSubject(incoming) != coreauth.AccountSubject(a) {
			return errors.New("verified principal or organization changed")
		}
		if _, e = os.Stat(filepath.Join(n.authDir, root.FileName)); e != nil {
			return errors.New("account removed")
		}
		updated, e := h.authManager.PatchAuth(ctx, a.ID, a.RegistrationEpoch, func(latest *coreauth.Auth) error {
			if latest.Generation != a.Generation || latest.CredentialVersion != a.CredentialVersion || !latest.Disabled {
				return errors.New("account changed")
			}
			// Provider-owned token/identity fields are replaced. Existing device,
			// thread, routing, binding and operator metadata retain precedence.
			replace := map[string]bool{"access_token": true, "refresh_token": true, "id_token": true, "expired": true, "last_refresh": true, "email": true, "account_id": true, "account_uuid": true, "organization_uuid": true, "organization_name": true}
			for key, value := range current.Credentials {
				_, exists := latest.Metadata[key]
				if replace[key] || !exists {
					latest.Metadata[key] = value
				}
			}
			latest.Metadata["console_renewal_id"] = r.ID
			latest.Storage = nil
			return nil
		})
		if e != nil {
			return e
		}
		if updated == nil {
			return errors.New("account changed")
		}
		if h.postAuthPersistHook != nil {
			if e = h.postAuthPersistHook(ctx, updated); e != nil {
				return e
			}
		}
	}
	now := time.Now()
	current.Status = "accepted"
	current.FileName = root.FileName
	current.ReviewedAt = &now
	current.Credentials = nil
	return selfServiceWrite(n.authDir+".self-service-oauth", current)
}
