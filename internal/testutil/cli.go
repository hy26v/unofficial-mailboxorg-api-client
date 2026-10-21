// ABOUTME: CLI-invocation test helper: runs a command function with injected stdin/stdout/stderr
// ABOUTME: and captures the resulting output streams and exit code for assertions.
package testutil

import (
	"bytes"
	"strings"
)

// CLIResult captures the outcome of a CLI invocation.
type CLIResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// RunCLI runs run with the given stdin content and args, capturing stdout/stderr/exit code.
// run mirrors a main-style entry point: it writes to the given writers and returns an exit code.
func RunCLI(run func(stdin *strings.Reader, stdout, stderr *bytes.Buffer, args []string) int, stdin string, args []string) CLIResult {
	in := strings.NewReader(stdin)
	var out, errOut bytes.Buffer

	code := run(in, &out, &errOut, args)

	return CLIResult{
		Stdout:   out.String(),
		Stderr:   errOut.String(),
		ExitCode: code,
	}
}
