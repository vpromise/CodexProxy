package fileperm

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAtomicCredentialSyncErrorsDistinguishReplacement(t *testing.T) {
	for _, stage := range []string{"temp", "directory"} {
		t.Run(stage, func(t *testing.T) {
			if stage == "directory" && runtime.GOOS == "windows" {
				t.Skip("directory sync is not available on Windows")
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			const old = "previous"
			const next = "replacement"
			if errWrite := os.WriteFile(path, []byte(old), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			syncErr := errors.New("injected sync failure")
			replaced, errWrite := writePrivateFileAtomic(path, []byte(next), true, func(file *os.File) error {
				if file.Name() == dir {
					return syncErr
				}
				checkCredentialBytes(t, path, old)
				checkCredentialBytes(t, file.Name(), next)
				if stage == "temp" {
					return syncErr
				}
				return file.Sync()
			})
			if !errors.Is(errWrite, syncErr) || replaced != (stage == "directory") {
				t.Fatalf("replacement result = %t, %v", replaced, errWrite)
			}
			if replaced {
				if !strings.Contains(errWrite.Error(), "durability is unconfirmed") {
					t.Fatalf("ambiguous durability was not reported: %v", errWrite)
				}
				checkCredentialBytes(t, path, next)
			} else {
				checkCredentialBytes(t, path, old)
			}
			entries, errList := os.ReadDir(dir)
			if errList != nil || len(entries) != 1 {
				t.Fatalf("temp file remained after failure: entries=%d, error=%v", len(entries), errList)
			}
		})
	}
}

func TestAtomicCredentialPreservesRemovedOrReplacedDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test holds an open handle while removing the original")
	}
	for _, mode := range []string{"removed", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "auth.json")
			if errWrite := os.WriteFile(path, []byte("previous"), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			old, errOpen := os.Open(path)
			if errOpen != nil {
				t.Fatal(errOpen)
			}
			defer func() { _ = old.Close() }()
			replaced, errWrite := writePrivateFileAtomic(path, []byte("stale"), true, func(file *os.File) error {
				if errRemove := os.Remove(path); errRemove != nil {
					t.Fatal(errRemove)
				}
				if mode == "replaced" {
					if errWrite := os.WriteFile(path, []byte("newer"), 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
				}
				return file.Sync()
			})
			if errWrite == nil || replaced || !strings.Contains(errWrite.Error(), "destination changed") {
				t.Fatalf("stale write result = %t, %v", replaced, errWrite)
			}
			if mode == "replaced" {
				checkCredentialBytes(t, path, "newer")
			} else if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
				t.Fatalf("removed file was recreated: %v", errStat)
			}
		})
	}
}

func TestAtomicCredentialFollowsRelativeSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks can require additional privileges on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	if errLink := os.Symlink("relative-target.json", path); errLink != nil {
		t.Fatal(errLink)
	}

	for _, value := range []string{"new", "updated"} {
		replaced, errWrite := WritePrivateFileAtomic(path, []byte(value), value == "updated")
		if errWrite != nil || !replaced {
			t.Fatalf("write through alias %q = %t, %v", value, replaced, errWrite)
		}
		if target, errLink := os.Readlink(path); target != "relative-target.json" || errLink != nil {
			t.Fatalf("credential alias changed: %q, %v", target, errLink)
		}
		checkCredentialBytes(t, filepath.Join(dir, "relative-target.json"), value)
		info, errStat := os.Stat(path)
		if errStat != nil || info.Mode().Perm() != PrivateFileMode {
			t.Fatalf("unexpected destination permissions after save: %v, %v", info, errStat)
		}
	}
}

func checkCredentialBytes(t *testing.T, path, want string) {
	t.Helper()
	got, errRead := os.ReadFile(path)
	if errRead != nil || string(got) != want {
		t.Fatalf("unexpected contents for %s: size=%d, error=%v", filepath.Base(path), len(got), errRead)
	}
}
