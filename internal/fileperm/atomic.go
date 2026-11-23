package fileperm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// WritePrivateFileAtomic replaces a private regular file using a temporary
// sibling. The caller prepares the parent directory. The first result reports
// whether the replacement took effect, even if syncing the directory failed.
func WritePrivateFileAtomic(path string, data []byte, requireExisting bool) (bool, error) {
	return writePrivateFileAtomic(path, data, requireExisting, (*os.File).Sync)
}

func writePrivateFileAtomic(path string, data []byte, requireExisting bool, syncFile func(*os.File) error) (bool, error) {
	return writePrivateFileAtomicVersion(path, data, requireExisting, nil, syncFile)
}

// ErrCredentialFileChanged signals that an observed credential file can no longer
// be replaced safely.
var ErrCredentialFileChanged = errors.New("credential destination changed before replacement")

// WritePrivateFileAtomicIfCurrent replaces the observed version of an existing
// file. It checks the file identity and metadata again just before replacement.
func WritePrivateFileAtomicIfCurrent(path string, data []byte, observed os.FileInfo) (bool, error) {
	return writePrivateFileAtomicVersion(path, data, true, observed, (*os.File).Sync)
}

func writePrivateFileAtomicVersion(path string, data []byte, requireExisting bool, observed os.FileInfo, syncFile func(*os.File) error) (bool, error) {
	target, original, errResolve := atomicWriteDestination(path)
	if errResolve != nil {
		return false, fmt.Errorf("inspect credential destination: %w", errResolve)
	}
	if observed != nil && !sameFileVersion(observed, original) {
		return false, ErrCredentialFileChanged
	}
	if requireExisting && original == nil {
		return false, fmt.Errorf("credential destination no longer exists: %w", os.ErrNotExist)
	}

	dir := filepath.Dir(target)
	temp, errCreate := os.CreateTemp(dir, ".credential-*.tmp")
	if errCreate != nil {
		return false, fmt.Errorf("create private credential temp file: %w", errCreate)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}()

	if errMode := temp.Chmod(PrivateFileMode); errMode != nil {
		return false, fmt.Errorf("secure credential temp file: %w", errMode)
	}
	if _, errWrite := temp.Write(data); errWrite != nil {
		return false, fmt.Errorf("write credential temp file: %w", errWrite)
	}
	if errSync := syncFile(temp); errSync != nil {
		return false, fmt.Errorf("sync credential temp file: %w", errSync)
	}
	errClose := temp.Close()
	temp = nil
	if errClose != nil {
		return false, fmt.Errorf("close credential temp file: %w", errClose)
	}

	current, errStat := os.Lstat(target)
	if errStat != nil && !os.IsNotExist(errStat) {
		return false, fmt.Errorf("recheck credential destination: %w", errStat)
	}
	if (original == nil && current != nil) || (original != nil && (current == nil || !os.SameFile(original, current))) {
		return false, ErrCredentialFileChanged
	}
	if observed != nil && !sameFileVersion(observed, current) {
		return false, ErrCredentialFileChanged
	}
	if errRename := os.Rename(tempPath, target); errRename != nil {
		return false, fmt.Errorf("replace credential file: %w", errRename)
	}
	tempPath = ""

	if errSync := syncCredentialDirectory(dir, syncFile); errSync != nil {
		return true, fmt.Errorf("credential file replaced but durability is unconfirmed: %w", errSync)
	}
	return true, nil
}

func sameFileVersion(observed, current os.FileInfo) bool {
	return current != nil && os.SameFile(observed, current) &&
		observed.Size() == current.Size() && observed.ModTime().Equal(current.ModTime())
}

// SyncPrivateFileDirectory confirms the directory entry for an existing file,
// including when a caller saves the same content after an uncertain rename.
func SyncPrivateFileDirectory(path string) error {
	target, info, errResolve := atomicWriteDestination(path)
	if errResolve != nil {
		return errResolve
	}
	if info == nil {
		return os.ErrNotExist
	}
	return syncCredentialDirectory(filepath.Dir(target), (*os.File).Sync)
}

func syncCredentialDirectory(dir string, syncFile func(*os.File) error) error {
	// Windows does not support syncing a directory through an os.File.
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, errOpen := os.Open(dir)
	if errOpen != nil {
		return fmt.Errorf("open credential directory: %w", errOpen)
	}
	errSync := syncFile(directory)
	errClose := directory.Close()
	if errSync != nil {
		return fmt.Errorf("sync credential directory: %w", errSync)
	}
	return errClose
}

// Resolve leaf symlinks so the replacement updates the file they point to.
func atomicWriteDestination(path string) (string, os.FileInfo, error) {
	for range 40 {
		info, errStat := os.Lstat(path)
		if os.IsNotExist(errStat) {
			return path, nil, nil
		}
		if errStat != nil {
			return "", nil, errStat
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if !info.Mode().IsRegular() {
				return "", nil, fmt.Errorf("credential destination is not a regular file")
			}
			return path, info, nil
		}

		link, errRead := os.Readlink(path)
		if errRead != nil {
			return "", nil, errRead
		}
		if filepath.IsAbs(link) {
			path = link
			continue
		}
		parent, errParent := filepath.EvalSymlinks(filepath.Dir(path))
		if errParent != nil {
			return "", nil, errParent
		}
		path = filepath.Join(parent, link)
	}
	return "", nil, fmt.Errorf("too many credential symlinks")
}
