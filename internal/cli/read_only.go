// ABOUTME: Read-only mode: refuses every command and flag that changes account data.
// ABOUTME: Turned on by safety.read_only in config.toml or MBX_READ_ONLY; checked before any request.
package cli

import (
	"fmt"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
)

const (
	readOnlySetting = "safety.read_only"
	readOnlyEnv     = "MBX_READ_ONLY"
)

// readOnlySessionOperations change only the CLI's own session, not account data. They
// stay available in read-only mode, because no read can run without a session.
var readOnlySessionOperations = map[string]bool{"login": true, "logout": true}

// readOnlySource reports whether read-only mode is on and names what turned it on. The
// environment variable can only turn the mode on: a configured read_only = true stays in
// force whatever MBX_READ_ONLY says. An unrecognized value fails closed.
func readOnlySource(configured bool, getenv func(string) string) (string, *cliErrors.Error) {
	env := strings.ToLower(strings.TrimSpace(getenv(readOnlyEnv)))
	envOn := false
	switch env {
	case "", "0", "false", "no", "off":
	case "1", "true", "yes", "on":
		envOn = true
	default:
		return "", &cliErrors.Error{Code: "invalid_configuration", Message: readOnlyEnv + " must be 1 (on) or 0 (no change); it cannot turn off " + readOnlySetting}
	}
	switch {
	case configured:
		return readOnlySetting + " in config.toml", nil
	case envOn:
		return readOnlyEnv, nil
	}
	return "", nil
}

// commandReadOnlyError refuses, before authentication or network access, a command that
// would change account data while read-only mode is on: every write or destructive
// operation, and every write flag of a read operation (mail read --mark-seen). Dry runs
// and preflights send no change, so they stay available.
func commandReadOnlyError(args []string, configured bool, getenv func(string) string) *cliErrors.Error {
	source, err := readOnlySource(configured, getenv)
	if err != nil || source == "" {
		return err
	}
	op, registered, flagStart := operationForArgs(args)
	if !registered || readOnlySessionOperations[op.Name] || commandRequestsDryRun(args, op, flagStart) {
		return nil
	}
	if op.SideEffect != operations.SideEffectRead {
		message := fmt.Sprintf("%s changes account data, and read-only mode is on (%s)", op.Name, source)
		if _, ok := op.Args["dry-run"]; ok {
			message += "; --dry-run is still available"
		}
		return &cliErrors.Error{Code: "read_only", Message: message}
	}
	if flag := commandWriteFlag(args, op, flagStart); flag != "" {
		return &cliErrors.Error{
			Code:    "read_only",
			Message: fmt.Sprintf("--%s of %s changes account data, and read-only mode is on (%s); run the command without --%s", flag, op.Name, source, flag),
		}
	}
	return nil
}

// ReadOnlyEnabled reports whether read-only mode is on for the configured value and the
// process environment. Long-running callers such as "mbx mcp" use it to decide which
// tools to offer; every command still checks the mode again before it runs.
func ReadOnlyEnabled(configured bool, getenv func(string) string) (bool, error) {
	source, err := readOnlySource(configured, getenv)
	if err != nil {
		return false, err
	}
	return source != "", nil
}
