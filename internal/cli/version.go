// ABOUTME: Reports the mbx version for "mbx version" and "mbx --version", and the value sent
// ABOUTME: to the server at login. Release builds set it with -ldflags; other builds use build info.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

// version is set for release binaries with
// -ldflags "-X github.com/hy26v/unofficial-mailboxorg-api-client/internal/cli.version=v1.2.3".
// It stays empty in other builds, which then use the module version from the build info.
var version string

// clientVersion is the version sent to the server with each login request.
var clientVersion = strings.TrimPrefix(currentVersion().Version, "v")

type versionInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit,omitempty"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

func currentVersion() versionInfo {
	info, ok := debug.ReadBuildInfo()
	return resolveVersion(version, info, ok)
}

// resolveVersion prefers the release value, then the module version that "go install
// module@version" and "go build" in a Git checkout record, and reports "devel" otherwise.
func resolveVersion(release string, info *debug.BuildInfo, ok bool) versionInfo {
	result := versionInfo{Version: "devel", Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH}
	if ok && info != nil {
		if info.GoVersion != "" {
			result.Go = info.GoVersion
		}
		if v := info.Main.Version; v != "" && v != "(devel)" {
			result.Version = v
		}
		modified := false
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				result.Commit = setting.Value
			case "vcs.modified":
				modified = setting.Value == "true"
			}
		}
		if result.Commit != "" && modified {
			result.Commit += "-dirty"
		}
	}
	if release != "" {
		result.Version = release
	}
	return result
}

func isVersionRequest(args []string) bool {
	return len(args) > 0 && (args[0] == "version" || args[0] == "--version")
}

// runVersion needs no configuration, session or network, like the discovery commands.
func runVersion(stdout, stderr io.Writer, args []string, output outputOptions) int {
	if len(args) > 0 {
		err := &cliErrors.Error{Code: "invalid_argument", Message: "version takes no arguments; got " + args[0]}
		writeErr(stderr, output.structured, err)
		return cliErrors.ExitCodeFor(err)
	}
	info := currentVersion()
	if output.structured {
		return runStructured(stdout, stderr, output, func(out, _ io.Writer) int {
			encoded, _ := json.Marshal(info)
			fmt.Fprintln(out, string(encoded))
			return 0
		})
	}
	line := fmt.Sprintf("%s %s (%s, %s", clientName, info.Version, info.Go, info.Platform)
	if info.Commit != "" {
		line += ", commit " + info.Commit
	}
	fmt.Fprintln(stdout, line+")")
	return 0
}

// Version returns the version that "mbx version" reports.
func Version() string {
	return currentVersion().Version
}
