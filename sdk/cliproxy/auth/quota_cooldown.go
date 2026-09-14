package auth

import (
	"errors"
	"net/http"
	"strings"
	"time"
)

// IsQuotaCooldownError reports a pool cooldown caused entirely by known quota
// limits. Other temporary failures retain their existing protocol handling.
func IsQuotaCooldownError(err error) bool {
	var cooldownErr *modelCooldownError
	return errors.As(err, &cooldownErr) && cooldownErr != nil && cooldownErr.quotaLimited
}

// quotaCooldownForModel classifies an already-blocked candidate without changing
// its eligibility or deadline. A quota must cover the effective blocking window;
// an expired quota cannot relabel a later transport failure as rate limiting.
func quotaCooldownForModel(auth *Auth, model string, now, blockedUntil time.Time) bool {
	if auth == nil || !blockedUntil.After(now) {
		return false
	}
	if auth.Quota.Reason == "credential_quota" && quotaCoversCooldown(auth.Quota, auth.LastError, auth.NextRetryAfter, now, blockedUntil) {
		return true
	}
	if model != "" && len(auth.ModelStates) > 0 {
		modelKey := canonicalModelKey(model)
		for stateModel, state := range auth.ModelStates {
			if state != nil && canonicalModelKey(stateModel) == modelKey && quotaCoversCooldown(state.Quota, state.LastError, state.NextRetryAfter, now, blockedUntil) {
				return true
			}
		}
		return false
	}
	return quotaCoversCooldown(auth.Quota, auth.LastError, auth.NextRetryAfter, now, blockedUntil)
}

func quotaCoversCooldown(quota QuotaState, lastErr *Error, retryAt, now, blockedUntil time.Time) bool {
	if !quota.Exceeded {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(quota.Reason)) {
	case "quota", "credential_quota":
	case "":
		// Older snapshots may lack a reason; require explicit rate-limit evidence.
		if lastErr == nil || lastErr.HTTPStatus != http.StatusTooManyRequests {
			return false
		}
	default:
		// Cloudflare challenges also use QuotaState for backoff bookkeeping.
		return false
	}
	reset := quota.NextRecoverAt
	if reset.IsZero() {
		reset = retryAt
	}
	return reset.After(now) && !reset.Before(blockedUntil)
}
