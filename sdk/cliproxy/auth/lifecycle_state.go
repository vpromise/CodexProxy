package auth

import (
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// ApplyModelRegistration commits a prepared registry mutation only while its
// credential registration and routing configuration are still current. The
// callback must only mutate the registry; it must not call Manager methods or
// perform I/O. Availability/statistics changes do not invalidate model discovery.
func (m *Manager) ApplyModelRegistration(snapshot *Auth, apply func()) bool {
	if m == nil || snapshot == nil || apply == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.auths[snapshot.ID]
	if snapshot.RegistrationEpoch != 0 && (current == nil || current.RegistrationEpoch != snapshot.RegistrationEpoch) {
		return false
	}
	if current != nil && (current.Disabled != snapshot.Disabled || (current.Status == StatusDisabled) != (snapshot.Status == StatusDisabled) ||
		current.Provider != snapshot.Provider || current.Prefix != snapshot.Prefix || current.AuthKind() != snapshot.AuthKind() ||
		!reflect.DeepEqual(current.Attributes, snapshot.Attributes)) {
		return false
	}
	apply()
	return true
}

func (m *Manager) currentRequestAuth(selected *Auth) (*Auth, error) {
	if selected == nil {
		return nil, &Error{Code: "auth_not_found", Message: "credential unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	m.mu.RLock()
	current := m.auths[selected.ID]
	if selected.RegistrationEpoch != 0 && (current == nil || current.RegistrationEpoch != selected.RegistrationEpoch) {
		m.mu.RUnlock()
		return nil, &Error{Code: "auth_not_found", Message: "credential registration changed", HTTPStatus: http.StatusServiceUnavailable}
	}
	if current == nil {
		current = selected
	}
	snapshot := current.Clone()
	m.mu.RUnlock()
	if snapshot.Disabled || snapshot.Status == StatusDisabled {
		return nil, &Error{Code: "auth_disabled", Message: "credential disabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	return snapshot, nil
}

func bindResultAuth(result Result, auth *Auth) Result {
	if auth != nil {
		result.RegistrationEpoch = auth.RegistrationEpoch
		result.authSnapshot = auth.Clone()
	}
	return result
}

func resultMatchesRegistration(result Result, auth *Auth) bool {
	return auth != nil && (result.RegistrationEpoch == 0 || result.RegistrationEpoch == auth.RegistrationEpoch)
}

// publishAuthModelStates holds the manager read lock through registry publication.
// A removal cannot relabel an old snapshot with a replacement registry epoch.
// Use current state so a concurrent statistics-only update cannot suppress a
// pending availability publication merely by incrementing the generation.
func (m *Manager) publishAuthModelStates(snapshot *Auth, expectedRegistryEpoch ...uint64) {
	if m == nil || snapshot == nil {
		return
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	current := m.auths[snapshot.ID]
	if current == nil || current.RegistrationEpoch != snapshot.RegistrationEpoch {
		return
	}
	reg := registry.GetGlobalRegistry()
	models, epoch := reg.GetModelsAndEpochForClient(current.ID)
	if len(expectedRegistryEpoch) > 0 && epoch != expectedRegistryEpoch[0] {
		return
	}
	projections := make([]registry.ClientModelProjection, 0, len(models))
	now := time.Now()
	for _, model := range models {
		if model != nil && strings.TrimSpace(model.ID) != "" {
			projections = append(projections, m.clientModelProjectionForAuth(current, model.ID, now))
		}
	}
	reg.ApplyClientModelProjections(current.ID, epoch, current.Generation, projections)
}

func resultMatchesAvailabilityEpoch(result Result, auth *Auth) bool {
	return auth != nil && (result.authSnapshot == nil || result.authSnapshot.availabilityEpoch == auth.availabilityEpoch)
}
