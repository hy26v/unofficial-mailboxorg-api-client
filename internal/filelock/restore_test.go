// ABOUTME: Tests that restoring a moved-aside token-file lock never replaces or deletes a lock
// ABOUTME: it does not own, with and without hard-link support.
package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errLinkUnsupported = errors.New("link: not supported")

func linkUnsupported(string, string) error { return errLinkUnsupported }

func writeAsideLock(t *testing.T, dir, token string, modTime time.Time) string {
	t.Helper()
	aside := filepath.Join(dir, "mbx.lock.release.x")
	if err := os.WriteFile(aside, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(aside, modTime, modTime); err != nil {
		t.Fatal(err)
	}
	return aside
}

func TestRestoreLockWith_WithoutHardLinksRestoresContentAndAge(t *testing.T) {
	dir := t.TempDir()
	modTime := time.Now().Add(-time.Hour).Truncate(time.Second)
	aside := writeAsideLock(t, dir, "holder-token", modTime)
	path := filepath.Join(dir, "mbx.lock")

	if err := restoreLockWith(aside, path, linkUnsupported); err != nil {
		t.Fatalf("restore: %v", err)
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "holder-token" {
		t.Fatalf("restored lock = %q err=%v, want the holder's token", content, err)
	}
	if info, err := os.Stat(path); err != nil || !info.ModTime().Equal(modTime) {
		t.Errorf("restored lock time = %v err=%v, want %v", info.ModTime(), err, modTime)
	}
	if _, err := os.Stat(aside); !os.IsNotExist(err) {
		t.Errorf("aside file still exists after a restore: %v", err)
	}
}

func TestRestoreLockWith_NewerLockIsKeptAndAsideIsNotDeleted(t *testing.T) {
	for name, link := range map[string]func(string, string) error{
		"hard links":    os.Link,
		"no hard links": linkUnsupported,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			aside := writeAsideLock(t, dir, "earlier-holder", time.Now())
			path := filepath.Join(dir, "mbx.lock")
			if err := os.WriteFile(path, []byte("newer-holder"), 0o600); err != nil {
				t.Fatal(err)
			}

			err := restoreLockWith(aside, path, link)
			if err == nil || !strings.Contains(err.Error(), aside) {
				t.Fatalf("restore error = %v, want an error that names the kept aside file", err)
			}
			if content, _ := os.ReadFile(path); string(content) != "newer-holder" {
				t.Errorf("newer lock = %q, want it unchanged", content)
			}
			if content, err := os.ReadFile(aside); err != nil || string(content) != "earlier-holder" {
				t.Errorf("aside lock = %q err=%v, want it kept", content, err)
			}
		})
	}
}

func TestRestoreLockWith_FailedRestoreKeepsAside(t *testing.T) {
	dir := t.TempDir()
	aside := writeAsideLock(t, dir, "holder-token", time.Now())
	path := filepath.Join(dir, "missing-dir", "mbx.lock")

	if err := restoreLockWith(aside, path, linkUnsupported); err == nil {
		t.Fatal("restore into a missing directory succeeded")
	}
	if content, err := os.ReadFile(aside); err != nil || string(content) != "holder-token" {
		t.Errorf("aside lock = %q err=%v, want it kept", content, err)
	}
}
