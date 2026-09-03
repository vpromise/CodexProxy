// Package fileperm centralizes restrictive permissions for files containing
// credentials, configuration secrets, or request data.
package fileperm

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	PrivateDirMode  os.FileMode = 0o700
	PrivateFileMode os.FileMode = 0o600
)

// EnsurePrivateDir creates a directory and restricts an existing leaf
// directory. It avoids changing the process working directory or a filesystem
// root when a caller supplies a bare filename or root-level path.
func EnsurePrivateDir(path string) error {
	clean := filepath.Clean(path)
	if errMkdir := os.MkdirAll(clean, PrivateDirMode); errMkdir != nil {
		return errMkdir
	}
	if clean == "." || filepath.Dir(clean) == clean {
		return nil
	}
	if errChmod := os.Chmod(clean, PrivateDirMode); errChmod != nil {
		return fmt.Errorf("restrict directory permissions: %w", errChmod)
	}
	return nil
}

// EnsurePrivateParent creates and restricts the parent of filePath.
func EnsurePrivateParent(filePath string) error {
	return EnsurePrivateDir(filepath.Dir(filePath))
}

// OpenPrivateFile opens a file and restricts its mode before callers write
// sensitive content. The explicit chmod also fixes pre-existing broad modes.
func OpenPrivateFile(path string, flag int) (*os.File, error) {
	openFlag := flag &^ os.O_TRUNC
	file, errOpen := os.OpenFile(path, openFlag, PrivateFileMode)
	if errOpen != nil {
		return nil, errOpen
	}
	if errChmod := file.Chmod(PrivateFileMode); errChmod != nil {
		_ = file.Close()
		return nil, fmt.Errorf("restrict file permissions: %w", errChmod)
	}
	if flag&os.O_TRUNC != 0 {
		if errTruncate := file.Truncate(0); errTruncate != nil {
			_ = file.Close()
			return nil, fmt.Errorf("truncate private file: %w", errTruncate)
		}
		if _, errSeek := file.Seek(0, 0); errSeek != nil {
			_ = file.Close()
			return nil, fmt.Errorf("rewind private file: %w", errSeek)
		}
	}
	return file, nil
}

// WritePrivateFile replaces a sensitive file using mode 0600 and also
// restricts an existing destination before writing its new contents.
func WritePrivateFile(path string, data []byte) error {
	file, errOpen := OpenPrivateFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if errOpen != nil {
		return errOpen
	}
	if _, errWrite := file.Write(data); errWrite != nil {
		_ = file.Close()
		return errWrite
	}
	return file.Close()
}

// RestrictPrivateFile changes an existing sensitive file to mode 0600.
func RestrictPrivateFile(path string) error {
	if errChmod := os.Chmod(path, PrivateFileMode); errChmod != nil {
		return fmt.Errorf("restrict file permissions: %w", errChmod)
	}
	return nil
}
