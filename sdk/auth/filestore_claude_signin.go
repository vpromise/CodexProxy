package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/fileperm"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const claudeSignInFileSuffix = ".claude-signin"

type claudeSignInFile struct {
	Version int `json:"version"`
	cliproxyauth.ClaudeSignInRecord
}

func claudeSignInPath(root, credentialPath string) string {
	if root == "" || !strings.EqualFold(filepath.Ext(credentialPath), ".json") {
		return ""
	}
	base, errBase := filepath.Abs(root)
	source, errSource := filepath.Abs(credentialPath)
	if errBase != nil || errSource != nil {
		return ""
	}
	relative, errRelative := filepath.Rel(base, source)
	if errRelative != nil || !filepath.IsLocal(relative) {
		return ""
	}
	return source + claudeSignInFileSuffix
}

// LoadClaudeSignIn reads the private, non-credential state associated with an auth file.
func (s *FileTokenStore) LoadClaudeSignIn(auth *cliproxyauth.Auth) (cliproxyauth.ClaudeSignInRecord, error) {
	if auth == nil {
		return cliproxyauth.ClaudeSignInRecord{}, nil
	}
	path := claudeSignInPath(s.baseDirSnapshot(), auth.Attributes[cliproxyauth.AttributePath])
	if path == "" {
		return cliproxyauth.ClaudeSignInRecord{}, nil
	}
	return readClaudeSignInFile(path)
}

func readClaudeSignInFile(path string) (cliproxyauth.ClaudeSignInRecord, error) {
	var state claudeSignInFile
	info, errStat := os.Lstat(path)
	if errors.Is(errStat, os.ErrNotExist) {
		return state.ClaudeSignInRecord, nil
	}
	if errStat != nil {
		return state.ClaudeSignInRecord, errStat
	}
	if !info.Mode().IsRegular() || info.Size() > 1024 {
		return state.ClaudeSignInRecord, fmt.Errorf("invalid Claude sign-in state file")
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		return state.ClaudeSignInRecord, errRead
	}
	if errDecode := json.Unmarshal(raw, &state); errDecode != nil || state.Version != 1 {
		return cliproxyauth.ClaudeSignInRecord{}, fmt.Errorf("invalid Claude sign-in state record")
	}
	return state.ClaudeSignInRecord, nil
}

// SaveClaudeSignIn writes the marker only while the file still holds the observed OAuth tokens.
func (s *FileTokenStore) SaveClaudeSignIn(auth *cliproxyauth.Auth, record cliproxyauth.ClaudeSignInRecord) error {
	if record.CredentialFingerprint == "" || record.CredentialFingerprint != cliproxyauth.ClaudeSignInFingerprint(auth) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	source := auth.Attributes[cliproxyauth.AttributePath]
	path := claudeSignInPath(s.baseDir, source)
	if path == "" {
		return nil
	}
	raw, errRead := os.ReadFile(source)
	if errors.Is(errRead, os.ErrNotExist) {
		return nil
	}
	if errRead != nil {
		return errRead
	}
	var disk cliproxyauth.Auth
	if errDecode := json.Unmarshal(raw, &disk.Metadata); errDecode != nil {
		return fmt.Errorf("cannot verify Claude sign-in source")
	}
	disk.Provider, _ = disk.Metadata["type"].(string)
	if cliproxyauth.ClaudeSignInFingerprint(&disk) != record.CredentialFingerprint {
		return nil
	}
	info, errStat := os.Lstat(path)
	if errStat == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("Claude sign-in state destination is not a regular file")
	}
	if errStat != nil && !errors.Is(errStat, os.ErrNotExist) {
		return errStat
	}
	if prior, errPrior := readClaudeSignInFile(path); errPrior == nil &&
		prior.CredentialFingerprint == record.CredentialFingerprint && prior.AccessRejected {
		record.AccessRejected = true
	}
	data, errMarshal := json.Marshal(claudeSignInFile{Version: 1, ClaudeSignInRecord: record})
	if errMarshal != nil {
		return errMarshal
	}
	if errRestrict := fileperm.RestrictExistingPrivateParent(path); errRestrict != nil {
		return errRestrict
	}
	_, errWrite := fileperm.WritePrivateFileAtomic(path, data, false)
	return errWrite
}
