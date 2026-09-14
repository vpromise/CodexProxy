package auth

import (
	"net/http"
	"reflect"
	"strings"
	"time"
)

const unauthorizedCooldown = 30 * time.Minute

// preserveIncomingAuthCooldown retains runtime availability missing from a
// newly parsed credential file. Editing credential material is not evidence of
// quota or upstream capacity recovery.
func preserveIncomingAuthCooldown(existing, incoming *Auth, now time.Time) {
	if existing.NonUnauthorizedRetryAfter.After(incoming.NonUnauthorizedRetryAfter) {
		incoming.NonUnauthorizedRetryAfter = existing.NonUnauthorizedRetryAfter
	}
	quotaActive := existing.Quota.Exceeded && (existing.Quota.NextRecoverAt.IsZero() || existing.Quota.NextRecoverAt.After(now))
	if quotaActive && (!incoming.Quota.Exceeded || existing.Quota.NextRecoverAt.IsZero() || existing.Quota.NextRecoverAt.After(incoming.Quota.NextRecoverAt)) {
		incoming.Quota = existing.Quota.Clone()
	}
	if existing.Unavailable && (existing.NextRetryAfter.After(now) || quotaActive || isUnauthorizedState(existing.LastError, existing.StatusMessage)) {
		incoming.Unavailable = true
		if existing.NextRetryAfter.After(incoming.NextRetryAfter) {
			incoming.NextRetryAfter = existing.NextRetryAfter
		}
		if incoming.LastError == nil {
			incoming.LastError = cloneError(existing.LastError)
			incoming.StatusMessage = existing.StatusMessage
			incoming.UpdatedAt = existing.UpdatedAt
		}
		incoming.Status = StatusError
	}
}

func isUnauthorizedState(lastError *Error, message string) bool {
	if lastError != nil {
		if status := lastError.StatusCode(); status != 0 {
			return status == http.StatusUnauthorized
		}
		return strings.EqualFold(lastError.Code, "unauthorized") || isUnauthorizedError(lastError)
	}
	return strings.EqualFold(strings.TrimSpace(message), "unauthorized")
}

// remainingUnauthorizedCooldown retains quota and any deadline that could not
// have been created by the fixed 401 cooldown. A longer retained deadline may
// belong to an earlier capacity or model-support failure.
func remainingUnauthorizedCooldown(next, independent, updatedAt time.Time, quota QuotaState, now time.Time) (time.Time, bool) {
	remaining := time.Time{}
	if independent.After(now) {
		remaining = independent
	}
	quotaActive := quota.Exceeded && (quota.NextRecoverAt.IsZero() || quota.NextRecoverAt.After(now))
	if quotaActive && quota.NextRecoverAt.After(remaining) {
		remaining = quota.NextRecoverAt
	}
	if independent.IsZero() && !updatedAt.IsZero() && next.After(now) && next.After(updatedAt.Add(unauthorizedCooldown)) && next.After(remaining) {
		remaining = next
	}
	return remaining, quotaActive || remaining.After(now)
}

// ClearUnauthorizedModelStates retires obsolete 401 state without resetting
// independent quotas, other errors, or a model's explicit disabled state.
func ClearUnauthorizedModelStates(auth *Auth, now time.Time) []string {
	return clearUnauthorizedModelStates(auth, now)
}

func clearUnauthorizedModelStates(auth *Auth, now time.Time) []string {
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
		return nil
	}
	var changed []string
	for model, state := range auth.ModelStates {
		if state == nil || state.Status == StatusDisabled || !isUnauthorizedState(state.LastError, state.StatusMessage) {
			continue
		}
		next, blocked := remainingUnauthorizedCooldown(state.NextRetryAfter, state.NonUnauthorizedRetryAfter, state.UpdatedAt, state.Quota, now)
		state.LastError = nil
		state.StatusMessage = ""
		state.Status = StatusActive
		state.NextRetryAfter = next
		state.Unavailable = blocked
		state.UpdatedAt = now
		if blocked {
			state.Status = StatusError
			state.StatusMessage = "cooldown"
			if state.Quota.Exceeded {
				state.StatusMessage = state.Quota.Reason
			}
		} else {
			applyCooldownFields(&state.Quota, QuotaState{})
		}
		changed = append(changed, model)
	}
	if len(changed) > 0 {
		// Model aggregation must not discard an independent auth-level block.
		quota, retry, unavailable := auth.Quota.Clone(), auth.NextRetryAfter, auth.Unavailable
		independent := !isUnauthorizedState(auth.LastError, auth.StatusMessage) && unavailable && retry.After(now)
		for _, state := range auth.ModelStates {
			if state != nil && state.LastError != nil && reflect.DeepEqual(state.LastError, auth.LastError) {
				// This is the summary of a model error, not a separate auth failure.
				independent = false
				break
			}
		}
		quotaActive := quota.Exceeded && (quota.NextRecoverAt.IsZero() || quota.NextRecoverAt.After(now))
		updateAggregatedAvailability(auth, now)
		if quotaActive {
			auth.Quota = quota
		}
		if independent {
			auth.NextRetryAfter, auth.Unavailable = retry, unavailable
		}
	}
	return changed
}

func clearUnauthorizedAuthState(auth *Auth, now time.Time) bool {
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
		return false
	}
	unauthorized := isUnauthorizedState(auth.LastError, auth.StatusMessage)
	next, blocked := remainingUnauthorizedCooldown(auth.NextRetryAfter, auth.NonUnauthorizedRetryAfter, auth.UpdatedAt, auth.Quota, now)
	changed := len(clearUnauthorizedModelStates(auth, now)) > 0
	if !unauthorized {
		return changed
	}
	auth.LastError = nil
	auth.StatusMessage = ""
	auth.Status = StatusActive
	if len(auth.ModelStates) == 0 {
		auth.NextRetryAfter, auth.Unavailable = next, blocked
	} else {
		updateAggregatedAvailability(auth, now)
		if blocked && (auth.Quota.Reason == "credential_quota" || auth.Unavailable) {
			auth.Unavailable = true
			if next.After(auth.NextRetryAfter) {
				auth.NextRetryAfter = next
			}
		}
	}
	if auth.Unavailable || hasModelError(auth, now) {
		auth.Status = StatusError
		if auth.Quota.Exceeded {
			auth.StatusMessage = auth.Quota.Reason
		}
	}
	return true
}

// rememberIndependentCooldown runs before the overall monotonic deadline merge,
// so a previous 401 cannot contaminate the deadline of a shorter capacity error.
func rememberIndependentCooldown(previous, next time.Time, lastError *Error, message string) time.Time {
	if next.IsZero() {
		return time.Time{}
	}
	if !isUnauthorizedState(lastError, message) && next.After(previous) {
		return next
	}
	return previous
}
