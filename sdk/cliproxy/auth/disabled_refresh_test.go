package auth

import (
	"context"
	"testing"
	"time"
)

func TestDisabledAuthLeavesRefreshScheduleAndCanReenable(t *testing.T) {
	for _, statusOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled_flag", true: "disabled_status"}[statusOnly], func(t *testing.T) {
			ctx := context.Background()
			now := time.Now()
			m := NewManager(nil, nil, nil)
			exec := &countingRefreshExecutor{id: "codex"}
			m.RegisterExecutor(exec)
			auth := &Auth{ID: "disabled-fixture", Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "old", "refresh_token": "fixture", "expired": now.Add(-time.Hour).Format(time.RFC3339), "refresh_interval_seconds": 60}}
			if statusOnly {
				auth.Status = StatusDisabled
			} else {
				auth.Disabled = true
			}
			saved, err := m.Register(ctx, auth)
			if err != nil {
				t.Fatal(err)
			}
			loop := newAuthAutoRefreshLoop(m, time.Second, 1)
			m.refreshLoop = loop
			loop.upsert(saved.ID, now)
			loop.queueReschedule(saved.ID)
			loop.applyDirty(now)
			if _, ok := loop.index[saved.ID]; ok {
				t.Error("disabled auth remains in refresh heap")
			}
			for i := 0; i < 3; i++ {
				loop.handleDueAuth(ctx, now.Add(time.Duration(i)*2*time.Minute), saved.ID)
			}
			if len(loop.jobs) != 0 {
				t.Errorf("queued disabled jobs=%d", len(loop.jobs))
			}
			latest, _ := m.GetByID(saved.ID)
			if latest.Generation != saved.Generation {
				t.Errorf("disabled generation %d -> %d", saved.Generation, latest.Generation)
			}
			if m.shouldRefresh(latest, now) {
				t.Error("disabled credential eligible for refresh")
			}
			if m.markRefreshPending(saved.ID, now.Add(10*time.Minute)) {
				t.Error("disabled credential marked pending")
			}
			for len(loop.jobs) > 0 {
				m.refreshAuth(ctx, <-loop.jobs)
			}
			if exec.refreshCalls.Load() != 0 {
				t.Fatal("disabled token refreshed")
			}
			_, err = m.PatchAuth(ctx, saved.ID, saved.RegistrationEpoch, func(a *Auth) error { a.Disabled = false; a.Status = StatusActive; return nil })
			if err != nil {
				t.Fatal(err)
			}
			future := now.Add(20 * time.Minute)
			loop.applyDirty(future)
			if _, ok := loop.index[saved.ID]; !ok {
				t.Fatal("reenabled auth was not scheduled")
			}
			loop.handleDueAuth(ctx, future, saved.ID)
			select {
			case id := <-loop.jobs:
				m.refreshAuth(ctx, id)
			default:
				t.Fatal("reenabled auth has no refresh job")
			}
			if exec.refreshCalls.Load() != 1 {
				t.Fatalf("reenabled refresh calls=%d", exec.refreshCalls.Load())
			}
		})
	}
}
