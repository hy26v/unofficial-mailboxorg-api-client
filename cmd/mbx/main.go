// ABOUTME: mbx CLI entry point: resolves cross-platform user config/state paths and
// ABOUTME: wires process I/O, environment access, and the real HTTP client into the CLI.
package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	// Event time zones use time.LoadLocation. Embed the IANA zone database so it also
	// works where the system has none, such as Windows without a Go installation.
	_ "time/tzdata"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/cli"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/config"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/requestguard"
)

const baseURL = "https://office.mailbox.org/ajax"

func main() {
	os.Exit(run())
}

func run() int {
	paths, err := config.ResolvePaths(os.Getenv, os.UserHomeDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		return 1
	}
	httpClient := newHTTPClient(paths.Root)
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		return runMCP(os.Args[2:], paths, httpClient, os.Getenv, os.Stdout, os.Stderr)
	}

	return cli.RunWithLoginPrompts(
		os.Stdout,
		os.Stderr,
		os.Args[1:],
		httpClient,
		baseURL,
		paths.ConfigFile,
		paths.SessionFile,
		os.Getenv,
		terminalLoginPrompts(os.Stdin, os.Stderr),
	)
}

func newHTTPClient(stateRoot string) *http.Client {
	return &http.Client{Transport: requestguard.New(
		http.DefaultTransport,
		filepath.Join(stateRoot, "request-guard.json"),
	)}
}
