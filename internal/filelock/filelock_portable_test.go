//go:build aix || js || plan9 || wasip1

// ABOUTME: Tests the token-file lock fallback on the platforms that use it, for example js.
// ABOUTME: Restoring another holder's lock must work without hard links and never delete it.
package filelock

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreLock_PutsAnotherHoldersLockBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mbx.lock")
	aside := path + ".release.mine"
	if err := os.WriteFile(aside, []byte("other-holder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreLock(aside, path); err != nil {
		t.Fatalf("restoreLock: %v", err)
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "other-holder" {
		t.Fatalf("lock = %q err=%v, want the other holder's token back at the lock path", content, err)
	}
}

func TestRestoreLock_KeepsTheAsideLockWhenANewerLockExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mbx.lock")
	aside := path + ".release.mine"
	if err := os.WriteFile(aside, []byte("earlier-holder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("newer-holder"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreLock(aside, path); err == nil {
		t.Fatal("restoreLock reported success although a newer lock is at the path")
	}
	if content, err := os.ReadFile(aside); err != nil || string(content) != "earlier-holder" {
		t.Fatalf("aside lock = %q err=%v, want it kept, not deleted", content, err)
	}
	if content, _ := os.ReadFile(path); string(content) != "newer-holder" {
		t.Fatalf("newer lock = %q, want it unchanged", content)
	}
}
