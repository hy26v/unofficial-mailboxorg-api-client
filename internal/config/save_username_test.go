// ABOUTME: Tests that saving the prompted username edits config.toml in place.
// ABOUTME: Comments, other settings, line endings and symlinked config files must survive.
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readText(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSaveUsername_KeepsCommentsAndAddsKeyBeforeFirstTable(t *testing.T) {
	original := "# mbx settings, managed by hand\n\n" +
		"# Protect everything.\n[safety]\ndelete_protection = [\"all\"] # never relax\n\n" +
		"[state]\n# shared with the backup job\nsession_file = \"sessions/test.json\"\n"
	path := writeConfig(t, original)

	if _, err := SaveUsername(path, "dogfood@example.org"); err != nil {
		t.Fatalf("SaveUsername failed: %v", err)
	}
	want := "# mbx settings, managed by hand\n\n" +
		"username = 'dogfood@example.org'\n\n" +
		"# Protect everything.\n[safety]\ndelete_protection = [\"all\"] # never relax\n\n" +
		"[state]\n# shared with the backup job\nsession_file = \"sessions/test.json\"\n"
	if got := readText(t, path); got != want {
		t.Fatalf("config text =\n%s\nwant\n%s", got, want)
	}
}

func TestSaveUsername_FillsBlankUsernameKeepingItsComment(t *testing.T) {
	path := writeConfig(t, "username = \"\" # filled in by mbx login\r\n[safety]\r\ndelete_protection = []\r\n")
	if _, err := SaveUsername(path, "crlf@example.org"); err != nil {
		t.Fatalf("SaveUsername failed: %v", err)
	}
	want := "username = 'crlf@example.org' # filled in by mbx login\r\n[safety]\r\ndelete_protection = []\r\n"
	if got := readText(t, path); got != want {
		t.Fatalf("config text = %q, want %q", got, want)
	}
}

func TestSaveUsername_AppendsWithFileLineEndingsWhenThereAreNoTables(t *testing.T) {
	path := writeConfig(t, "# only a comment\r\n# and another")
	if _, err := SaveUsername(path, "end@example.org"); err != nil {
		t.Fatalf("SaveUsername failed: %v", err)
	}
	want := "# only a comment\r\n# and another\r\nusername = 'end@example.org'\r\n"
	if got := readText(t, path); got != want {
		t.Fatalf("config text = %q, want %q", got, want)
	}
}

func TestSaveUsername_WritesThroughSymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "dotfiles")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(realDir, "mbx.toml")
	if err := os.WriteFile(realPath, []byte("# tracked in dotfiles\n[safety]\ndelete_protection = [\"all\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, ".mbx")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "config.toml")
	if err := os.Symlink(realPath, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := SaveUsername(link, "linked@example.org"); err != nil {
		t.Fatalf("SaveUsername failed: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config path = %v, %v; want the symlink kept", info, err)
	}
	got := readText(t, realPath)
	if !strings.Contains(got, "# tracked in dotfiles") || !strings.Contains(got, "username = 'linked@example.org'") {
		t.Fatalf("link target text = %q, want comment kept and username added", got)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 1 {
		t.Fatalf("config directory has %d entries, want only the symlink", len(entries))
	}
}

func TestSaveUsername_RejectsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(filepath.Join(dir, "missing.toml"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := SaveUsername(link, "dangling@example.org"); err == nil {
		t.Fatal("SaveUsername() replaced a dangling symlink")
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config path = %v, %v; want the symlink untouched", info, err)
	}
}
