package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
)

var accessOperationSequence atomic.Uint64

func accessAuditReference(value string) string {
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// auditAccessDecision records routing evidence without credentials, filenames,
// emails, caller-provided model strings, session hints, or request bodies.
func auditAccessDecision(ctx context.Context, account *Auth, decision, reason string) {
	scope := AccessScopeFromContext(ctx)
	if !scope.Enabled() {
		return
	}
	fields := log.Fields{
		"access_operation": scope.operation,
		"access_event":     decision,
		"client_ref":       scope.caller,
		"pool_ref":         accessAuditReference(scope.pool),
		"policy_revision":  scope.revision,
		"provider":         scope.provider,
		"reason":           reason,
	}
	if account != nil {
		fields["account_ref"] = AccountIdentity(account)
	}
	entry := logEntryWithRequestID(ctx).WithFields(fields)
	if decision == "denied" {
		entry.Warn("account isolation decision")
	} else {
		entry.Info("account isolation decision")
	}
}
