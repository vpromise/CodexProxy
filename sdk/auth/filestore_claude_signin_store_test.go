package auth

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestFileTokenStoreClaudeSignInGuardsSourceAndCleansManagedDeletion(t *testing.T) {
	const name = "claude-state-source.json"
	const old = `{"type":"claude","access_token":"test-access-one","refresh_token":"test-refresh-one"}`
	const replacement = `{"type":"claude","access_token":"test-access-two","refresh_token":"test-refresh-one"}`
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	marker := path + claudeSignInFileSuffix
	writeCredential := func(raw string) {
		t.Helper()
		if errWrite := os.WriteFile(path, []byte(raw), 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	store := NewFileTokenStore()
	store.SetBaseDir(dir)
	writeCredential(old)
	fixture, errRead := store.readAuthFile(path, dir)
	if errRead != nil || fixture == nil {
		t.Fatalf("cannot load fixture: %v", errRead)
	}
	record := cliproxyauth.ClaudeSignInRecord{
		CredentialFingerprint: cliproxyauth.ClaudeSignInFingerprint(fixture),
		AccessRejected:        true,
	}

	writeCredential(replacement)
	if errSave := store.SaveClaudeSignIn(fixture, record); errSave != nil {
		t.Fatal(errSave)
	}
	if _, errStat := os.Stat(marker); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("old completion wrote a marker for a different file credential: %v", errStat)
	}

	writeCredential(old)
	if errSave := store.SaveClaudeSignIn(fixture, record); errSave != nil {
		t.Fatal(errSave)
	}
	raw, errRead := os.ReadFile(marker)
	if errRead != nil || bytes.Contains(raw, []byte("test-access-one")) || bytes.Contains(raw, []byte("test-refresh-one")) {
		t.Fatalf("local marker is missing or contains credential values: %v", errRead)
	}
	info, errStat := os.Stat(marker)
	if errStat != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("marker permissions are not private: %v", errStat)
	}
	record.AccessRejected = false
	if errSave := store.SaveClaudeSignIn(fixture, record); errSave != nil {
		t.Fatal(errSave)
	}
	if saved, errLoad := store.LoadClaudeSignIn(fixture); errLoad != nil || !saved.AccessRejected {
		t.Fatalf("later refresh-only state downgraded verified access rejection: %v", errLoad)
	}

	if errWrite := os.WriteFile(marker, []byte(`{"version":2,"access_rejected":true}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	restarted := cliproxyauth.NewManager(store, nil, nil)
	if errLoad := restarted.Load(t.Context()); errLoad != nil {
		t.Fatal(errLoad)
	}
	if auth, present := restarted.GetByID(name); !present || auth.RequiresSignIn() {
		t.Fatal("invalid marker affected loading of the credential")
	}
	if errDelete := store.Delete(t.Context(), name); errDelete != nil {
		t.Fatal(errDelete)
	}
	if _, errStat := os.Stat(marker); !errors.Is(errStat, os.ErrNotExist) {
		t.Fatalf("managed deletion left its sign-in state behind: %v", errStat)
	}
}
