// ABOUTME: Tests for cross-platform config paths, strict TOML loading, and session paths.
// ABOUTME: Also covers credential fallback behavior and secret redaction.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolvePaths_DefaultsToUserHome(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "users", "test")

	paths, err := ResolvePaths(func(string) string { return "" }, func() (string, error) {
		return home, nil
	})
	if err != nil {
		t.Fatalf("ResolvePaths failed: %v", err)
	}

	wantRoot := filepath.Join(home, ".mbx")
	if paths.Root != wantRoot {
		t.Errorf("Root = %q, want %q", paths.Root, wantRoot)
	}
	if paths.ConfigFile != filepath.Join(wantRoot, "config.toml") {
		t.Errorf("ConfigFile = %q", paths.ConfigFile)
	}
	if paths.SessionFile != filepath.Join(wantRoot, "session.json") {
		t.Errorf("SessionFile = %q", paths.SessionFile)
	}
}

func TestResolvePaths_AbsoluteMBXHomeRelocatesAllState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "portable-mbx")

	paths, err := ResolvePaths(
		func(key string) string {
			if key == "MBX_HOME" {
				return root
			}
			return ""
		},
		func() (string, error) {
			return "", errors.New("home lookup must not be called")
		},
	)
	if err != nil {
		t.Fatalf("ResolvePaths failed: %v", err)
	}
	if paths.Root != root {
		t.Errorf("Root = %q, want %q", paths.Root, root)
	}
	if paths.ConfigFile != filepath.Join(root, "config.toml") {
		t.Errorf("ConfigFile = %q", paths.ConfigFile)
	}
	if paths.SessionFile != filepath.Join(root, "session.json") {
		t.Errorf("SessionFile = %q", paths.SessionFile)
	}
}

func TestResolvePaths_RejectsRelativeMBXHome(t *testing.T) {
	_, err := ResolvePaths(
		func(key string) string {
			if key == "MBX_HOME" {
				return "relative/path"
			}
			return ""
		},
		func() (string, error) { return "", nil },
	)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v, want absolute-path error", err)
	}
}

func TestResolvePaths_RejectsNULInMBXHome(t *testing.T) {
	root := filepath.Join(t.TempDir(), "invalid\x00root")

	_, err := ResolvePaths(
		func(key string) string {
			if key == "MBX_HOME" {
				return root
			}
			return ""
		},
		func() (string, error) { return "", errors.New("home lookup must not be called") },
	)

	if err == nil || !strings.Contains(err.Error(), "NUL") {
		t.Fatalf("error = %v, want NUL-path error", err)
	}
}

func TestResolvePaths_HomeLookupFailure(t *testing.T) {
	_, err := ResolvePaths(
		func(string) string { return "" },
		func() (string, error) { return "", errors.New("no home") },
	)
	if err == nil || !strings.Contains(err.Error(), "home") {
		t.Fatalf("error = %v, want home lookup error", err)
	}
}

func TestResolvePaths_RejectsEmptyHome(t *testing.T) {
	_, err := ResolvePaths(
		func(string) string { return "" },
		func() (string, error) { return "", nil },
	)
	if err == nil || !strings.Contains(err.Error(), "empty path") {
		t.Fatalf("error = %v, want empty-home error", err)
	}
}

func TestResolvePaths_RejectsRelativeHome(t *testing.T) {
	_, err := ResolvePaths(
		func(string) string { return "" },
		func() (string, error) { return "relative/home", nil },
	)
	if err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("error = %v, want absolute-home error", err)
	}
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

func TestLoad_MissingFileReturnsEmptySettings(t *testing.T) {
	settings, err := Load(filepath.Join(t.TempDir(), "missing.toml"))
	if err != nil {
		t.Fatalf("Load missing file failed: %v", err)
	}
	if settings.Username != "" || len(settings.Safety.DeleteProtection) != 0 || settings.State.SessionFile != "" {
		t.Fatalf("settings = %#v, want zero value", settings)
	}
}

func TestLoad_StrictTOMLSettings(t *testing.T) {
	path := writeConfig(t, `
username = " fixture-user@example.invalid "

[safety]
delete_protection = ["mail", "folders"]

[state]
session_file = " sessions/account.json "
`)

	settings, err := Load(path)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if settings.Username != "fixture-user@example.invalid" {
		t.Errorf("Username = %q", settings.Username)
	}
	if got := strings.Join(settings.Safety.DeleteProtection, ","); got != "mail,folders" {
		t.Errorf("DeleteProtection = %q", got)
	}
	if settings.State.SessionFile != "sessions/account.json" {
		t.Errorf("SessionFile = %q", settings.State.SessionFile)
	}
}

func TestLoad_RejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, `
[safety]
delete_protecton = ["all"]
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded, want unknown-field error")
	}
	if !strings.Contains(err.Error(), "delete_protecton") {
		t.Fatalf("error = %v, want misspelled field", err)
	}
}

func TestLoad_RejectsPasswordField(t *testing.T) {
	path := writeConfig(t, `password = "do-not-store-secrets-here"`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded, want password field rejection")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Fatalf("error = %v, want password field rejection", err)
	}
	if strings.Contains(err.Error(), "do-not-store-secrets-here") {
		t.Fatalf("error leaked rejected password value: %v", err)
	}
}

func TestLoad_RejectsMalformedTOML(t *testing.T) {
	path := writeConfig(t, `username = "unterminated`)

	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "invalid config") {
		t.Fatalf("error = %v, want invalid config error", err)
	}
}

func TestLoad_RejectsNonRegularFile(t *testing.T) {
	_, err := Load(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v, want regular-file error", err)
	}
}

func TestSaveUsername_CreatesPrivateConfigAndParent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "new-home")
	path := filepath.Join(root, "config.toml")

	username, err := SaveUsername(path, "  first.user+mbx@example.org  ")
	if err != nil {
		t.Fatalf("SaveUsername failed: %v", err)
	}
	if username != "first.user+mbx@example.org" {
		t.Fatalf("username = %q, want trimmed address", username)
	}

	settings, err := Load(path)
	if err != nil {
		t.Fatalf("Load saved config failed: %v", err)
	}
	if settings.Username != username {
		t.Fatalf("saved username = %q, want %q", settings.Username, username)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat saved config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat config directory: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("config directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
}

func TestSaveUsername_PreservesSafetyAndStateSettings(t *testing.T) {
	path := writeConfig(t, `
# This comment and the supported setting values must survive.
[safety]
delete_protection = ["all"]

[state]
session_file = "sessions/test.json"
`)

	if _, err := SaveUsername(path, "dogfood@example.org"); err != nil {
		t.Fatalf("SaveUsername failed: %v", err)
	}
	settings, err := Load(path)
	if err != nil {
		t.Fatalf("Load saved config failed: %v", err)
	}
	if settings.Username != "dogfood@example.org" {
		t.Fatalf("username = %q", settings.Username)
	}
	if got := strings.Join(settings.Safety.DeleteProtection, ","); got != "all" {
		t.Fatalf("delete protection = %q, want all", got)
	}
	if settings.State.SessionFile != "sessions/test.json" {
		t.Fatalf("session file = %q", settings.State.SessionFile)
	}
}

func TestSaveUsername_TightensExistingConfigPermissions(t *testing.T) {
	path := writeConfig(t, "[safety]\ndelete_protection = [\"all\"]\n")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod config: %v", err)
	}

	if _, err := SaveUsername(path, "private@example.org"); err != nil {
		t.Fatalf("SaveUsername failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
}

func TestSaveUsername_DoesNotOverwriteDifferentUsername(t *testing.T) {
	path := writeConfig(t, `username = "existing@example.org"`)

	if _, err := SaveUsername(path, "replacement@example.org"); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("error = %v, want overwrite refusal", err)
	}
	settings, err := Load(path)
	if err != nil {
		t.Fatalf("Load config failed: %v", err)
	}
	if settings.Username != "existing@example.org" {
		t.Fatalf("username = %q, existing value was overwritten", settings.Username)
	}
}

func TestSaveUsername_AcceptsMatchingConcurrentValue(t *testing.T) {
	path := writeConfig(t, `username = "same@example.org"`)

	username, err := SaveUsername(path, "same@example.org")
	if err != nil || username != "same@example.org" {
		t.Fatalf("SaveUsername = %q, %v", username, err)
	}
}

func TestSaveUsername_RejectsInvalidAddressesWithoutCreatingConfig(t *testing.T) {
	tests := []string{"", "no-at-sign", "two@@example.org", "local@", "@example.org", "space here@example.org", "local@.example.org", "local@example.org."}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if _, err := SaveUsername(path, value); err == nil {
				t.Fatalf("SaveUsername(%q) succeeded", value)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("config exists after rejected username: %v", err)
			}
		})
	}
}

func TestSettingsResolveSessionFile_RelativeToConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	settings := Settings{State: StateSettings{SessionFile: filepath.Join("sessions", "account.json")}}

	got, err := settings.ResolveSessionFile(configPath, filepath.Join(root, "default.json"))
	if err != nil {
		t.Fatalf("ResolveSessionFile failed: %v", err)
	}
	want := filepath.Join(root, "sessions", "account.json")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestSettingsResolveSessionFile_UsesDefaultWhenUnset(t *testing.T) {
	defaultPath := filepath.Join(t.TempDir(), "session.json")
	got, err := (Settings{}).ResolveSessionFile("config.toml", defaultPath)
	if err != nil {
		t.Fatalf("ResolveSessionFile failed: %v", err)
	}
	if got != defaultPath {
		t.Fatalf("path = %q, want %q", got, defaultPath)
	}
}

func TestSettingsEnvironment_ConfigUsernameOverridesEnvironment(t *testing.T) {
	settings := Settings{Username: "config@mailbox.org"}
	base := map[string]string{
		"MBX_USERNAME": "environment@mailbox.org",
		"MBX_PASSWORD": "secret",
	}
	getenv := settings.Environment(func(key string) string { return base[key] })

	if got := getenv("MBX_USERNAME"); got != "config@mailbox.org" {
		t.Errorf("MBX_USERNAME = %q", got)
	}
	if got := getenv("MBX_PASSWORD"); got != "secret" {
		t.Errorf("MBX_PASSWORD = %q", got)
	}
}

func TestLoadCredentials_FromEnv(t *testing.T) {
	env := map[string]string{
		"MBX_USERNAME": "fixture-user@example.invalid",
		"MBX_PASSWORD": "s3cret",
	}

	creds, err := LoadCredentials(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("LoadCredentials failed: %v", err)
	}
	if creds.Username != "fixture-user@example.invalid" {
		t.Errorf("Username = %q", creds.Username)
	}
	if creds.Password != "s3cret" {
		t.Errorf("Password = %q", creds.Password)
	}
}

func TestLoadCredentials_MissingUsername(t *testing.T) {
	env := map[string]string{"MBX_PASSWORD": "s3cret"}
	_, err := LoadCredentials(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected error for missing username")
	}
}

func TestLoadCredentials_RejectsWhitespaceOnlyUsername(t *testing.T) {
	env := map[string]string{
		"MBX_USERNAME": " \t ",
		"MBX_PASSWORD": "s3cret",
	}

	_, err := LoadCredentials(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "username") {
		t.Fatalf("error = %v, want missing-username error", err)
	}
}

func TestLoadCredentials_MissingPassword(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "u"}
	_, err := LoadCredentials(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected error for missing password")
	}
	if !strings.Contains(err.Error(), "not stored") {
		t.Fatalf("error = %v, want config secret-handling guidance", err)
	}
}

func TestLoadAuthenticationCredentials_AllowsMissingPassword(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "u"}
	creds, err := LoadAuthenticationCredentials(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("LoadAuthenticationCredentials failed: %v", err)
	}
	if creds.Username != "u" || creds.Password != "" {
		t.Fatalf("credentials = %#v, want username with empty optional password", creds)
	}
}

func TestLoadCredentials_MissingBoth(t *testing.T) {
	_, err := LoadCredentials(func(k string) string { return "" })
	if err == nil {
		t.Fatal("expected error for missing both")
	}
}

func TestLoadCredentials_NilEnvironment(t *testing.T) {
	_, err := LoadCredentials(nil)
	if err == nil || !strings.Contains(err.Error(), "missing credentials") {
		t.Fatalf("error = %v, want missing-credentials error", err)
	}
}

func TestCredentials_StringRedactsPassword(t *testing.T) {
	creds := Credentials{Username: "fixture-user@example.invalid", Password: "s3cret"}
	s := creds.String()
	if strings.Contains(s, "s3cret") {
		t.Errorf("String() leaked password: %q", s)
	}
	if !strings.Contains(s, "fixture-user@example.invalid") {
		t.Errorf("String() should still show username: %q", s)
	}
}

func TestCredentials_GoStringRedactsPassword(t *testing.T) {
	creds := Credentials{Username: "u", Password: "s3cret"}
	s := creds.GoString()
	if strings.Contains(s, "s3cret") {
		t.Errorf("GoString() leaked password: %q", s)
	}
}

func TestLoadMCPAllowedWrites(t *testing.T) {
	dir := t.TempDir()
	for text, want := range map[string]*[]string{
		"":                             nil,
		"[mcp]\nallowed_writes = []\n": {},
		"[mcp]\nallowed_writes = [\"mail.draft\"]\n": {"mail.draft"},
	} {
		path := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		settings, err := Load(path)
		if err != nil {
			t.Fatalf("Load(%q): %v", text, err)
		}
		got := settings.MCP.AllowedWrites
		if (got == nil) != (want == nil) || (got != nil && !slices.Equal(*got, *want)) {
			t.Errorf("Load(%q) allowed_writes = %v, want %v", text, got, want)
		}
	}
}
