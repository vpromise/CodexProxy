package registry

import (
	"testing"
	"time"
)

func TestModelProjectionExpiryRefreshesCachedDiscovery(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client", "claude", []*ModelInfo{{ID: "model", Object: "model"}})
	_, epoch := r.GetModelsAndEpochForClient("client")
	now := time.Now()
	deadline := now.Add(time.Hour)
	projection := ClientModelProjection{ModelID: "model", Suspended: true, SuspendReason: "upstream capacity", SuspendUntil: deadline}
	if !r.ApplyClientModelProjections("client", epoch, 1, []ClientModelProjection{projection}) {
		t.Fatal("projection was rejected")
	}
	if got := r.availableModelsAt("openai", now); len(got) != 0 {
		t.Fatal("model should be unavailable during a capacity cooldown")
	}
	if !r.availableModelsCache["openai"].expiresAt.Equal(deadline) {
		t.Fatal("cached discovery does not expire with the cooldown")
	}
	if got := r.availableModelsAt("openai", deadline); len(got) != 1 {
		t.Fatal("model did not recover without a new request or registration")
	}

	// A newer deadline must replace the cache expiry, while an older generation
	// cannot restore the earlier deadline or clear the current suspension.
	projection.SuspendUntil = deadline.Add(time.Hour)
	if !r.ApplyClientModelProjections("client", epoch, 2, []ClientModelProjection{projection}) {
		t.Fatal("extended cooldown was rejected")
	}
	if r.ApplyClientModelProjections("client", epoch, 1, []ClientModelProjection{{ModelID: "model"}}) {
		t.Fatal("stale projection cleared the newer cooldown")
	}
	if got := r.availableModelsAt("openai", deadline); len(got) != 0 {
		t.Fatal("extended cooldown reused an expired cache entry")
	}
	if got := r.availableModelsAt("openai", projection.SuspendUntil); len(got) != 1 {
		t.Fatal("extended cooldown did not expire")
	}
}

func TestModelProjectionQuotaDiscoveryAndHealthySibling(t *testing.T) {
	for _, reason := range []string{"quota", "credential_quota", "disabled"} {
		t.Run(reason, func(t *testing.T) {
			r := newTestModelRegistry()
			r.RegisterClient("limited", "claude", []*ModelInfo{{ID: "model", Object: "model"}})
			_, epoch := r.GetModelsAndEpochForClient("limited")
			deadline := time.Now().Add(time.Hour)
			projection := ClientModelProjection{ModelID: "model", Suspended: true, SuspendReason: reason,
				QuotaExceeded: true, QuotaRecoverAt: deadline}
			if reason != "disabled" {
				projection.SuspendUntil = deadline
			}
			r.ApplyClientModelProjections("limited", epoch, 1, []ClientModelProjection{projection})
			wantVisible := 1
			if reason == "disabled" {
				wantVisible = 0
			}
			if got := len(r.GetAvailableModels("openai")); got != wantVisible {
				t.Fatalf("visible models = %d, want %d", got, wantVisible)
			}
			if got := len(r.GetAvailableModelsByProvider("claude")); got != wantVisible {
				t.Fatalf("provider-visible models = %d, want %d", got, wantVisible)
			}
			if got := r.GetModelCount("model"); got != 0 {
				t.Fatalf("limited credential should not be selectable, got %d", got)
			}
			r.RegisterClient("healthy", "claude", []*ModelInfo{{ID: "model", Object: "model"}})
			if got := r.GetModelCount("model"); got != 1 {
				t.Fatalf("quota and suspension double-counted the unavailable client: %d", got)
			}
			if len(r.GetAvailableModels("openai")) != 1 || len(r.GetAvailableModelsByProvider("claude")) != 1 {
				t.Fatal("limited credential hid a healthy sibling")
			}
		})
	}
}

func TestModelProjectionExpiredDeadlinesAndManualSuspension(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client", "claude", []*ModelInfo{{ID: "model", Object: "model"}})
	_, epoch := r.GetModelsAndEpochForClient("client")
	past := time.Now().Add(-time.Hour)
	projection := ClientModelProjection{ModelID: "model", Suspended: true, SuspendReason: "quota", SuspendUntil: past,
		QuotaExceeded: true, QuotaRecoverAt: past}
	r.ApplyClientModelProjections("client", epoch, 1, []ClientModelProjection{projection})
	if r.IsModelSuspendedForClient("client", "model") || r.IsModelQuotaExceededForClient("client", "model") {
		t.Fatal("expired projection still blocks the model")
	}
	if r.GetModelCount("model") != 1 || len(r.GetAvailableModelsByProvider("claude")) != 1 {
		t.Fatal("availability readers did not recover with the projection deadline")
	}
	if model, err := r.GetFirstAvailableModel("openai"); err != nil || model != "model" {
		t.Fatalf("auto selection after expiry = %q, %v", model, err)
	}
	r.CleanupExpiredQuotas()
	if len(r.models["model"].quotaDeadlines) != 0 || len(r.models["model"].QuotaExceededClients) != 0 {
		t.Fatal("expired quota metadata was not cleaned up")
	}
	// Explicit suspension has no automatic expiry, even after a timed projection.
	r.SuspendClientModel("client", "model", "manual")
	if !r.IsModelSuspendedForClient("client", "model") || len(r.availableModelsAt("openai", time.Now().Add(24*time.Hour))) != 0 {
		t.Fatal("an old deadline cleared manual suspension")
	}
	r.ResumeClientModel("client", "model")
	if r.GetModelCount("model") != 1 {
		t.Fatal("manual resume did not restore availability")
	}
}

func TestModelProjectionQuotaDeadlineOverridesLegacyCleanupWindow(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client", "claude", []*ModelInfo{{ID: "model", Object: "model"}})
	_, epoch := r.GetModelsAndEpochForClient("client")
	deadline := time.Now().Add(time.Hour)
	r.ApplyClientModelProjections("client", epoch, 1, []ClientModelProjection{{ModelID: "model", QuotaExceeded: true, QuotaRecoverAt: deadline}})
	oldObservation := time.Now().Add(-10 * time.Minute)
	r.models["model"].QuotaExceededClients["client"] = &oldObservation
	r.CleanupExpiredQuotas()
	if !r.IsModelQuotaExceededForClient("client", "model") || r.GetModelCount("model") != 0 {
		t.Fatal("legacy five-minute window shortened a known quota deadline")
	}
	r.RegisterClient("client", "claude", []*ModelInfo{{ID: "model", Object: "model"}})
	if len(r.models["model"].quotaDeadlines) != 0 || len(r.models["model"].suspensionDeadlines) != 0 {
		t.Fatal("re-registration retained old projection deadlines")
	}
}

func TestModelProjectionExpiryAdvancesConsumerCacheGeneration(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client", "claude", []*ModelInfo{{ID: "model", Object: "model"}})
	_, epoch := r.GetModelsAndEpochForClient("client")
	before := time.Now().Add(-time.Hour)
	r.ApplyClientModelProjections("client", epoch, 1, []ClientModelProjection{{ModelID: "model", Suspended: true,
		SuspendReason: "capacity", SuspendUntil: before.Add(time.Minute)}})
	if len(r.availableModelsAt("openai", before)) != 0 {
		t.Fatal("expected the earlier discovery snapshot to omit the cooling model")
	}
	previousGeneration := r.generation
	if r.GetGeneration() <= previousGeneration {
		t.Fatal("consumer cache cannot observe cooldown expiry")
	}
	if len(r.GetAvailableModels("openai")) != 1 {
		t.Fatal("expired consumer cache did not recover the model")
	}
}
