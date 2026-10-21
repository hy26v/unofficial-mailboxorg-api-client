// ABOUTME: Tests for "mbx version": the release, build-info and devel version sources, human
// ABOUTME: and structured output, argument errors, and independence from configuration.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func runVersionCLI(t *testing.T, configPath string, args ...string) (string, string, int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(&stdout, &stderr, args, nil, "http://127.0.0.1:1/ajax", configPath,
		filepath.Join(t.TempDir(), "session.json"), func(string) string { return "" })
	return stdout.String(), stderr.String(), code
}

func TestResolveVersion_ReleaseValueWins(t *testing.T) {
	info := &debug.BuildInfo{GoVersion: "go1.99.0", Main: debug.Module{Version: "v0.0.1"}}
	got := resolveVersion("v1.2.3", info, true)
	if got.Version != "v1.2.3" || got.Go != "go1.99.0" {
		t.Fatalf("got %+v, want release version v1.2.3 with go1.99.0", got)
	}
}

func TestResolveVersion_ModuleVersionFromGoInstall(t *testing.T) {
	info := &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}}
	if got := resolveVersion("", info, true); got.Version != "v0.4.0" {
		t.Fatalf("version = %q, want v0.4.0", got.Version)
	}
}

func TestResolveVersion_DevelWithCommit(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "(devel)"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "abc123"},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	got := resolveVersion("", info, true)
	if got.Version != "devel" || got.Commit != "abc123-dirty" {
		t.Fatalf("got %+v, want devel with commit abc123-dirty", got)
	}
	if got.Platform != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("platform = %q", got.Platform)
	}
}

func TestResolveVersion_NoBuildInfo(t *testing.T) {
	got := resolveVersion("", nil, false)
	if got.Version != "devel" || got.Go != runtime.Version() || got.Commit != "" {
		t.Fatalf("got %+v, want devel with the runtime Go version and no commit", got)
	}
}

func TestVersionCommand_HumanOutput(t *testing.T) {
	saved := version
	version = "v9.8.7"
	defer func() { version = saved }()

	for _, args := range [][]string{{"version"}, {"--version"}} {
		stdout, stderr, code := runVersionCLI(t, filepath.Join(t.TempDir(), "config.toml"), args...)
		if code != 0 || stderr != "" {
			t.Fatalf("%v: code=%d stderr=%q", args, code, stderr)
		}
		want := "mbx v9.8.7 (" + runtime.Version()
		if !strings.HasPrefix(stdout, want) || !strings.Contains(stdout, runtime.GOOS+"/"+runtime.GOARCH) {
			t.Errorf("%v: stdout = %q, want prefix %q and the platform", args, stdout, want)
		}
	}
}

func TestVersionCommand_StructuredOutput(t *testing.T) {
	saved := version
	version = "v9.8.7"
	defer func() { version = saved }()

	stdout, stderr, code := runVersionCLI(t, filepath.Join(t.TempDir(), "config.toml"), "version", "--format", "json")
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %q", stdout)
	}
	if got["version"] != "v9.8.7" || got["go"] == "" || got["platform"] == "" {
		t.Errorf("got %v, want version, go and platform", got)
	}

	stdout, _, code = runVersionCLI(t, filepath.Join(t.TempDir(), "config.toml"), "version", "--format", "values", "--select", "version")
	if code != 0 || strings.TrimSpace(stdout) != "v9.8.7" {
		t.Errorf("values output = %q (code %d), want v9.8.7", stdout, code)
	}
}

func TestVersionCommand_RejectsArguments(t *testing.T) {
	_, stderr, code := runVersionCLI(t, filepath.Join(t.TempDir(), "config.toml"), "version", "extra")
	if code == 0 || !strings.Contains(stderr, "version takes no arguments") {
		t.Fatalf("code=%d stderr=%q, want an argument error", code, stderr)
	}
}

func TestVersionCommand_IgnoresBrokenConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(configPath, []byte("this is = = not toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runVersionCLI(t, configPath, "version")
	if code != 0 || !strings.HasPrefix(stdout, "mbx ") {
		t.Fatalf("code=%d stdout=%q stderr=%q, want the version despite a broken config", code, stdout, stderr)
	}
}

func TestClientVersion_HasNoVPrefix(t *testing.T) {
	if clientVersion == "" || strings.HasPrefix(clientVersion, "v") {
		t.Fatalf("clientVersion = %q, want a non-empty value without a v prefix", clientVersion)
	}
}
