package auth

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/fileperm"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

type codexFileVersion struct {
	target string
	digest [sha256.Size]byte
	info   os.FileInfo
}

func readCodexFileVersion(path string) (codexFileVersion, []byte, error) {
	target, errResolve := filepath.EvalSymlinks(path)
	if errResolve != nil {
		return codexFileVersion{}, nil, errResolve
	}
	resolvedInfo, errLstat := os.Lstat(target)
	if errLstat != nil {
		return codexFileVersion{}, nil, errLstat
	}
	if !resolvedInfo.Mode().IsRegular() {
		return codexFileVersion{}, nil, fmt.Errorf("Codex credential is not a regular file")
	}
	file, errOpen := os.Open(target)
	if errOpen != nil {
		return codexFileVersion{}, nil, errOpen
	}
	defer func() { _ = file.Close() }()
	info, errStat := file.Stat()
	if errStat != nil {
		return codexFileVersion{}, nil, errStat
	}
	if !os.SameFile(resolvedInfo, info) {
		return codexFileVersion{}, nil, fileperm.ErrCredentialFileChanged
	}
	raw, errRead := io.ReadAll(file)
	if errRead != nil {
		return codexFileVersion{}, nil, errRead
	}
	return codexFileVersion{target: target, digest: sha256.Sum256(raw), info: info}, raw, nil
}

// saveLocalCodexRefresh is called with the store locked.
func (s *FileTokenStore) saveLocalCodexRefresh(ctx context.Context, id, path string, raw []byte) (string, error) {
	expected, previous, errRead := readCodexFileVersion(path)
	if errRead != nil {
		return "", fmt.Errorf("auth filestore: read existing Codex credential: %w", errRead)
	}

	syncOnly := jsonEqual(previous, raw)
	if !syncOnly && !cliproxyauth.MatchesLocalCodexRefreshSource(ctx, previous) {
		return "", fmt.Errorf("auth filestore: verify Codex refresh source: %w", fileperm.ErrCredentialFileChanged)
	}
	errSave := fileperm.RestrictExistingPrivateParent(path)
	switch {
	case errSave != nil:
		if syncOnly {
			return "", fmt.Errorf("auth filestore: secure auth directory: %w", errSave)
		}
	case syncOnly:
		if errRestrict := fileperm.RestrictPrivateFile(path); errRestrict != nil {
			return "", fmt.Errorf("auth filestore: secure auth file: %w", errRestrict)
		}
		errSave = fileperm.SyncPrivateFileDirectory(path)
	default:
		var replaced bool
		replaced, errSave = fileperm.WritePrivateFileAtomicIfCurrent(path, raw, expected.info)
		if replaced {
			expected.digest = sha256.Sum256(raw)
			expected.info = nil
			syncOnly = true
		}
	}
	if errSave == nil {
		return path, nil
	}
	logger := log.WithField("auth_id", id)
	if syncOnly {
		logger.Warn("local Codex credential directory durability is unconfirmed")
	} else {
		logger.Warn("local Codex credential save failed before replacement")
	}
	errSave = fmt.Errorf("auth filestore: save Codex credentials: %w", errSave)
	if !errors.Is(errSave, fileperm.ErrCredentialFileChanged) && !errors.Is(errSave, os.ErrNotExist) {
		errSave = cliproxyauth.NewLocalCodexRefreshSaveError(errSave, previous, s.retryLocalCodexRefresh(path, expected, syncOnly))
	}
	if syncOnly {
		return path, errSave
	}
	return "", errSave
}

// The recovery closure retains file identity and digests, with no token payload.
func (s *FileTokenStore) retryLocalCodexRefresh(path string, expected codexFileVersion, syncOnly bool) func(context.Context, *cliproxyauth.Auth) (bool, error) {
	baseDir := s.baseDirSnapshot()
	return func(ctx context.Context, auth *cliproxyauth.Auth) (bool, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if errContext := ctx.Err(); errContext != nil {
			return false, errContext
		}
		if s.baseDirSnapshot() != baseDir {
			return false, nil
		}
		current, _, errRead := readCodexFileVersion(path)
		if errors.Is(errRead, os.ErrNotExist) || errors.Is(errRead, fileperm.ErrCredentialFileChanged) {
			return false, nil
		}
		if errRead != nil {
			return false, errRead
		}
		if current.target != expected.target || current.digest != expected.digest ||
			(expected.info != nil && !os.SameFile(expected.info, current.info)) {
			return false, nil
		}
		if syncOnly {
			errSync := fileperm.SyncPrivateFileDirectory(path)
			return errSync == nil, errSync
		}
		if errRestrict := fileperm.RestrictExistingPrivateParent(path); errRestrict != nil {
			return false, errRestrict
		}

		auth.Metadata["disabled"] = auth.Disabled
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return false, errMarshal
		}
		replaced, errWrite := fileperm.WritePrivateFileAtomicIfCurrent(path, raw, current.info)
		if replaced {
			expected.digest = sha256.Sum256(raw)
			expected.info = nil
			syncOnly = true
		} else if errors.Is(errWrite, fileperm.ErrCredentialFileChanged) || errors.Is(errWrite, os.ErrNotExist) {
			return false, nil
		}
		return errWrite == nil, errWrite
	}
}
