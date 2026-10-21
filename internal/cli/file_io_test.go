// ABOUTME: Regression tests for safe local destinations used by downloads and exports.
// ABOUTME: Ensures private output writes cannot follow links to unrelated files.
package cli

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWritePrivateFile_RejectsSymlinkWithoutTruncatingTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "download.txt")
	if err := os.WriteFile(target, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("seeding target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := writePrivateFile(link, []byte("mailbox contents")); err == nil {
		t.Fatal("writePrivateFile() followed a symlink, want an error")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading target: %v", err)
	}
	if string(got) != "keep me" {
		t.Fatalf("target = %q, want original content", got)
	}
}

func TestWritePrivateFile_ReplacesAtomicallyWithoutTruncatingInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow replacing a file that another handle has open")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "export.ics")
	if err := os.WriteFile(path, []byte("complete old export"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A reader that opened the destination before the write must keep seeing the complete
	// old content. Writing in place (truncate, then write) would show it an empty file.
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()

	if err := writePrivateFile(path, []byte("new")); err != nil {
		t.Fatalf("writePrivateFile() error = %v", err)
	}
	old, err := io.ReadAll(reader)
	if err != nil || string(old) != "complete old export" {
		t.Fatalf("earlier reader saw %q, %v; want the complete old content", old, err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "new" {
		t.Fatalf("destination = %q, %v; want new content", got, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("destination mode = %v, %v; want 0600", info.Mode().Perm(), err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 1 {
		t.Fatalf("directory entries = %v, %v; want no temporary file left", entries, err)
	}
}
