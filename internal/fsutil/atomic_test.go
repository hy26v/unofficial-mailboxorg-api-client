// ABOUTME: Tests for the shared atomic file replacement helper.
// ABOUTME: Covers content, private mode, link replacement and temp-file cleanup.
package fsutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWriteFileAtomic_ReplacesContentWithPrivateMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old content that is longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("WriteFileAtomic() error = %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("content = %q, %v; want new", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, %v; want 0600", info.Mode().Perm(), err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want only the target", entries, err)
	}
}

func TestWriteFileAtomic_ReplacesLinkInsteadOfWritingThroughIt(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := WriteFileAtomic(link, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(target); string(got) != "keep" {
		t.Fatalf("link target = %q, want unchanged", got)
	}
	info, err := os.Lstat(link)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("link path = %v, %v; want a regular file", info, err)
	}
}

func TestWriteFileAtomic_FailureLeavesTargetAndNoTempFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	// A file in the directory makes the rename over it fail on every platform.
	if err := os.WriteFile(filepath.Join(path, "child"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new"), 0o600); err == nil {
		t.Fatal("WriteFileAtomic() over a non-empty directory succeeded")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "target" {
		t.Fatalf("directory entries = %v, %v; want only the original target", entries, err)
	}
}
