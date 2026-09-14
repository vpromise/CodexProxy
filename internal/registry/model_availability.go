package registry

import (
	"strings"
	"time"
)

// Expiry changes the discovery snapshot even without a new auth publication.
// Advance the generation so consumers with their own caches also rebuild.
func (r *ModelRegistry) expireAvailableModelsCacheLocked(now time.Time) {
	for _, cache := range r.availableModelsCache {
		if !cache.expiresAt.IsZero() && !cache.expiresAt.After(now) {
			r.invalidateAvailableModelsCacheLocked()
			return
		}
	}
}

func setClientDeadline(deadlines *map[string]time.Time, clientID string, deadline time.Time) bool {
	previous, exists := (*deadlines)[clientID]
	if deadline.IsZero() {
		delete(*deadlines, clientID)
		return exists
	}
	if exists && previous.Equal(deadline) {
		return false
	}
	if *deadlines == nil {
		*deadlines = make(map[string]time.Time)
	}
	(*deadlines)[clientID] = deadline
	return true
}

func clientModelSuspension(registration *ModelRegistration, clientID string, now time.Time) (string, bool) {
	reason, suspended := registration.SuspendedClients[clientID]
	if deadline := registration.suspensionDeadlines[clientID]; !deadline.IsZero() && !deadline.After(now) {
		return "", false
	}
	return reason, suspended
}

func clientModelQuotaRecoveryAt(registration *ModelRegistration, clientID string, quotaTime *time.Time) time.Time {
	if quotaTime == nil {
		return time.Time{}
	}
	if deadline := registration.quotaDeadlines[clientID]; !deadline.IsZero() {
		return deadline
	}
	return quotaTime.Add(modelQuotaExceededWindow)
}

func isQuotaSuspension(reason string) bool {
	return strings.EqualFold(reason, "quota") || strings.EqualFold(reason, "credential_quota")
}
