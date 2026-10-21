// ABOUTME: Tests for the local session store: persisting and reloading the session id
// ABOUTME: and cookies, and redacting them from string output.
package client

import (
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionStore_SaveAndLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := &SessionStore{Path: filepath.Join(dir, "session.json")}

	want := Session{
		ID: "abc123",
		Cookies: []*http.Cookie{
			{Name: "JSESSIONID", Value: "xyz", Expires: time.Now().Add(time.Hour).UTC().Truncate(time.Second)},
		},
	}

	if err := store.Save(want); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got.ID != want.ID {
		t.Errorf("ID = %q, want %q", got.ID, want.ID)
	}
	if len(got.Cookies) != 1 || got.Cookies[0].Value != "xyz" {
		t.Errorf("Cookies = %+v", got.Cookies)
	}
}

func TestSessionStore_SaveCreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	store := &SessionStore{Path: filepath.Join(dir, "nested", "session.json")}

	if err := store.Save(Session{ID: "abc"}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got.ID != "abc" {
		t.Errorf("ID = %q, want abc", got.ID)
	}
}

func TestSessionStore_LoadMissingFileReturnsZeroValue(t *testing.T) {
	dir := t.TempDir()
	store := &SessionStore{Path: filepath.Join(dir, "nonexistent.json")}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load of missing file should not error, got: %v", err)
	}
	if got.ID != "" || len(got.Cookies) != 0 {
		t.Errorf("got = %+v, want zero value", got)
	}
}

func TestSessionStore_LoadRejectsNullDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte("null"), 0o600); err != nil {
		t.Fatalf("writing session fixture: %v", err)
	}

	store := &SessionStore{Path: path}
	if _, err := store.Load(); err == nil {
		t.Fatal("Load() accepted a null session document, want an error")
	}
}

func TestSessionStore_LoadRejectsOversizedDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	document := `{"id":"abc","padding":"` + strings.Repeat("x", maxSessionSize) + `"}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("writing oversized session fixture: %v", err)
	}

	store := &SessionStore{Path: path}
	if sess, err := store.Load(); err == nil {
		t.Fatalf("Load() accepted %d-byte session as %+v", len(document), sess)
	}
}

func TestSessionStore_LoadRejectsUnknownFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte(`{"id":"abc","cookes":[]}`), 0o600); err != nil {
		t.Fatalf("writing misspelled session fixture: %v", err)
	}

	store := &SessionStore{Path: path}
	if sess, err := store.Load(); err == nil {
		t.Fatalf("Load() ignored unknown field and returned %+v", sess)
	}
}

func TestSessionStore_LoadRejectsInvalidCookies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte(`{"id":"abc","cookies":[{"name":"","value":"secret"}]}`), 0o600); err != nil {
		t.Fatalf("writing invalid cookie fixture: %v", err)
	}

	store := &SessionStore{Path: path}
	if sess, err := store.Load(); err == nil {
		t.Fatalf("Load() accepted invalid cookie in %+v", sess)
	}
}

func TestSessionStore_SaveRejectsInvalidCookies(t *testing.T) {
	store := &SessionStore{Path: filepath.Join(t.TempDir(), "session.json")}
	err := store.Save(Session{
		ID:      "abc",
		Cookies: []*http.Cookie{{Name: "", Value: "secret"}},
	})
	if err == nil {
		t.Fatal("Save() accepted an invalid cookie, producing a session that Load cannot read")
	}
}

func TestSessionStore_SaveDoesNotOverflowLargeMaxAge(t *testing.T) {
	store := &SessionStore{Path: filepath.Join(t.TempDir(), "session.json")}
	if err := store.Save(Session{
		ID:      "abc",
		Cookies: []*http.Cookie{{Name: "long_lived", Value: "secret", MaxAge: math.MaxInt}},
	}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if len(got.Cookies) != 1 || got.Cookies[0].Name != "long_lived" {
		t.Fatalf("loaded cookies = %+v, want the positive-MaxAge cookie preserved", got.Cookies)
	}
}

func TestSessionStore_ClearRemovesFile(t *testing.T) {
	dir := t.TempDir()
	store := &SessionStore{Path: filepath.Join(dir, "session.json")}

	if err := store.Save(Session{ID: "abc123"}); err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load after Clear failed: %v", err)
	}
	if got.ID != "" || len(got.Cookies) != 0 {
		t.Errorf("got = %+v, want zero value after Clear", got)
	}
}

func TestSessionStore_ClearMissingFileIsNoError(t *testing.T) {
	dir := t.TempDir()
	store := &SessionStore{Path: filepath.Join(dir, "nonexistent.json")}

	if err := store.Clear(); err != nil {
		t.Fatalf("Clear of missing file should not error, got: %v", err)
	}
}

func TestSessionStore_ClearDoesNotRemoveDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}

	store := &SessionStore{Path: path}
	if err := store.Clear(); err == nil {
		t.Fatal("Clear() succeeded for a directory, want error")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("session directory was removed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("session path mode = %v, want directory", info.Mode())
	}
}

func TestSession_StringRedactsSessionIDAndCookies(t *testing.T) {
	s := Session{
		ID: "super-secret-session-id",
		Cookies: []*http.Cookie{
			{Name: "open-xchange-secret-1", Value: "topsecret"},
		},
	}
	out := s.String()
	if strings.Contains(out, "super-secret-session-id") {
		t.Errorf("String() leaked session id: %q", out)
	}
	if strings.Contains(out, "topsecret") {
		t.Errorf("String() leaked cookie value: %q", out)
	}
}

func TestSession_AllFormatVerbsRedactSessionIDAndCookies(t *testing.T) {
	s := Session{
		ID:       "super-secret-session-id",
		Username: "user@example.org",
		Cookies:  []*http.Cookie{{Name: "open-xchange-secret-1", Value: "topsecret"}},
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, s)
		if strings.Contains(out, "super-secret-session-id") || strings.Contains(out, "topsecret") {
			t.Errorf("fmt %s leaked a session secret: %q", verb, out)
		}
	}
	if out := fmt.Sprintf("%#v", []Session{s}); strings.Contains(out, "topsecret") {
		t.Errorf("fmt %%#v of a slice leaked a cookie value: %q", out)
	}
}

// TestSessionStore_Save_TightensPermissionsOnExistingFile is the regression test for a
// session file that stays readable by other users: os.WriteFile applies its mode only when
// it creates the file, so rewriting a session over a pre-existing world-readable path left
// the session id and cookies exposed.
func TestSessionStore_Save_TightensPermissionsOnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seeding session file: %v", err)
	}

	store := &SessionStore{Path: path}
	if err := store.Save(Session{ID: "sess-1", Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "abc"}}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("session file mode = %#o, want 0600", perm)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.ID != "sess-1" {
		t.Errorf("reloaded session id = %q, want sess-1", loaded.ID)
	}
}

// TestSessionStore_Save_LeavesNoStrayFiles pins that repeated saves leave exactly the
// session file behind, so the atomic-replace write never litters the mbx home directory.
func TestSessionStore_Save_LeavesNoStrayFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.json")
	store := &SessionStore{Path: path}
	if err := store.Save(Session{ID: "sess-1"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	if err := store.Save(Session{ID: strings.Repeat("x", 10)}); err != nil {
		t.Fatalf("second Save() error = %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("directory contains %v, want only the session file", names)
	}
}
