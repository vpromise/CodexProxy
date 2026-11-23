package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

var errTestCodexSave = errors.New("injected credential save failure")

type localCodexFailingStore struct {
	*memoryAuthTestStore
	disk  []byte
	retry func(context.Context, *Auth) (bool, error)
}

func (s *localCodexFailingStore) Save(ctx context.Context, auth *Auth) (string, error) {
	if IsRefreshPersistence(ctx) {
		return "", NewLocalCodexRefreshSaveError(errTestCodexSave, s.disk, s.retry)
	}
	return s.memoryAuthTestStore.Save(ctx, auth)
}

func makeLocalCodexFileSnapshot(t *testing.T, path string, raw []byte) *Auth {
	t.Helper()
	snapshot := &Auth{
		ID: filepath.Base(path), Provider: "codex", Status: StatusActive,
		Attributes: map[string]string{AttributeSourceBackend: AuthSourceFile, AttributePath: path},
	}
	if errDecode := json.Unmarshal(raw, &snapshot.Metadata); errDecode != nil {
		t.Fatal(errDecode)
	}
	if disabled, _ := snapshot.Metadata["disabled"].(bool); disabled {
		snapshot.Disabled, snapshot.Status = true, StatusDisabled
	}
	return snapshot
}

func makePendingLocalCodexSave(t *testing.T, retry func(context.Context, *Auth) (bool, error)) (*Manager, *Auth, []byte) {
	t.Helper()
	raw := []byte(`{"type":"codex","account_id":"subject","pool_account_id":"instance","access_token":"a1","refresh_token":"r1","note":"old"}`)
	path := filepath.Join(t.TempDir(), "codex.json")
	if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	store := &localCodexFailingStore{memoryAuthTestStore: newMemoryAuthTestStore(), disk: raw, retry: retry}
	manager := NewManager(store, nil, nil)
	base, errRegister := manager.Register(WithSkipPersist(t.Context()), makeLocalCodexFileSnapshot(t, path, raw))
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	updated := base.Clone()
	updated.Metadata["access_token"] = "a2"
	updated.Metadata["refresh_token"] = "r2"
	updated.LastRefreshedAt = time.Now()
	pending, errUpdate := manager.UpdateRefreshedAuth(t.Context(), base, updated)
	if !errors.Is(errUpdate, errTestCodexSave) {
		t.Fatalf("refresh save error = %v", errUpdate)
	}
	return manager, pending, raw
}

func TestLocalCodexRefreshSavePausesForDisabledAuthAndUsesLatestMetadata(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempt *Auth
		manager, pending, prev := makePendingLocalCodexSave(t, func(_ context.Context, next *Auth) (bool, error) {
			attempt = next
			return true, nil
		})
		manager.StartAutoRefresh(t.Context(), time.Minute)
		defer manager.StopAutoRefresh()
		before := makeLocalCodexFileSnapshot(t, pending.Attributes[AttributePath], prev)
		patch := func(disabled bool) {
			t.Helper()
			if _, errPatch := manager.PatchAuth(WithSkipPersist(t.Context()), pending.ID, pending.RegistrationEpoch, func(auth *Auth) error {
				auth.Disabled = disabled
				auth.Status = StatusActive
				if disabled {
					auth.Status = StatusDisabled
				}
				auth.Metadata["note"] = "latest runtime note"
				return nil
			}); errPatch != nil {
				t.Fatal(errPatch)
			}
		}
		patch(true)
		if _, errApply := manager.Update(WithSkipPersist(t.Context()), before.Clone()); errApply != nil {
			t.Fatal(errApply)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if attempt != nil {
			t.Fatal("a stale enabled snapshot started a disk write while manually disabled")
		}
		patch(false)
		time.Sleep(time.Second)
		synctest.Wait()
		if attempt == nil || authRefreshToken(attempt) != "r2" || attempt.Disabled ||
			attempt.Metadata["note"] != "latest runtime note" || AccountIdentity(attempt) != AccountIdentity(pending) {
			t.Fatal("recovery did not write the latest live credential and settings")
		}
		if _, errApply := manager.Update(WithSkipPersist(t.Context()), before); errApply != nil {
			t.Fatal(errApply)
		}
		current, _ := manager.GetByID(pending.ID)
		if authRefreshToken(current) != "r2" || current.Metadata["note"] != "latest runtime note" {
			t.Fatal("a queued pre-refresh file observation rolled back the recovered state")
		}
	})
}

func TestLocalCodexRefreshSaveRejectsOldWork(t *testing.T) {
	for _, scenario := range []string{"delete", "rename", "register-replacement", "replace-store", "verified-file-relogin", "stop"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				attempts := 0
				manager, pending, _ := makePendingLocalCodexSave(t, func(context.Context, *Auth) (bool, error) {
					attempts++
					return true, nil
				})
				manager.StartAutoRefresh(t.Context(), time.Minute)
				defer manager.StopAutoRefresh()
				ctx := WithSkipPersist(t.Context())
				switch scenario {
				case "delete":
					manager.Remove(ctx, pending.ID)
				case "rename":
					pending.Attributes[AttributePath] += ".moved"
					if _, errUpdate := manager.Update(ctx, pending); errUpdate != nil {
						t.Fatal(errUpdate)
					}
				case "register-replacement":
					if _, errRegister := manager.Register(ctx, pending.Clone()); errRegister != nil {
						t.Fatal(errRegister)
					}
				case "replace-store":
					manager.SetStore(newMemoryAuthTestStore())
				case "verified-file-relogin":
					raw := []byte(`{"type":"codex","account_id":"subject","pool_account_id":"instance","access_token":"real-a3","refresh_token":"real-r3"}`)
					path := pending.Attributes[AttributePath]
					if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
					snapshot := makeLocalCodexFileSnapshot(t, path, raw)
					current, errUpdate := manager.Update(ctx, snapshot)
					if errUpdate != nil || authRefreshToken(current) != "real-r3" {
						t.Fatal("a genuine file relogin was rejected")
					}
				case "stop":
					manager.StopAutoRefresh()
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				if attempts != 0 {
					t.Fatalf("a superseded or stopped job invoked the store %d times", attempts)
				}
			})
		})
	}
}

func TestLocalCodexRefreshSaveBackoffAndWatchEdits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var attempts []time.Time
		manager, pending, _ := makePendingLocalCodexSave(t, func(context.Context, *Auth) (bool, error) {
			attempts = append(attempts, time.Now())
			return false, errTestCodexSave
		})
		manager.StartAutoRefresh(t.Context(), time.Minute)
		defer manager.StopAutoRefresh()
		start := time.Now()
		time.Sleep(time.Minute)
		synctest.Wait()
		want := []time.Duration{time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second, 31 * time.Second}
		if len(attempts) != len(want) {
			t.Fatalf("recovery attempt count = %d, want %d", len(attempts), len(want))
		}
		for index, at := range attempts {
			if at.Sub(start) != want[index] {
				t.Fatalf("attempt %d ran at %v, want %v", index+1, at.Sub(start), want[index])
			}
		}
		path := pending.Attributes[AttributePath]
		raw := []byte(`{"type":"codex","account_id":"subject","pool_account_id":"instance","access_token":"a1","refresh_token":"r1","note":"changed on disk","disabled":true}`)
		if errWrite := os.WriteFile(path, raw, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		snapshot := makeLocalCodexFileSnapshot(t, path, raw)
		current, errUpdate := manager.Update(WithSkipPersist(t.Context()), snapshot)
		if errUpdate != nil || authRefreshToken(current) != "r2" || !current.Disabled ||
			current.Metadata["note"] != "changed on disk" || current.CredentialVersion != pending.CredentialVersion {
			t.Fatal("verified metadata-only disk edit was lost or restored the retired token")
		}
	})
}
