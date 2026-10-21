// ABOUTME: Contract test guarding against operation-registry/parser drift: every
// ABOUTME: registered ArgSpec key must match the flag spelling its command actually parses.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// TestDefault_ArgKeysAreKebabCase guards against documentation/parser drift: usageFor and
// describe --llm render a flag as "--" + the ArgSpec map key verbatim, and every command's
// actual flag parser accepts lowercase, hyphen-separated multi-word flags (e.g. --dry-run,
// --end-date), never underscored (--display_name) or camelCase (--dryRun) ones. A key that
// isn't kebab-case means the CLI's own discovery output tells callers to pass a flag the
// parser will reject as unknown - confirmed live against resources.create (registered
// display_name, parser accepted only display-name) and contacts.create (registered dryRun,
// parser accepted only dry-run).
func TestDefault_ArgKeysAreKebabCase(t *testing.T) {
	for _, op := range operations.Default.List() {
		for key := range op.Args {
			if strings.ContainsAny(key, "_ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
				t.Errorf("operation %q registers arg key %q, which is not kebab-case; flags must be lowercase and hyphen-separated", op.Name, key)
			}
		}
	}
}

// TestDefault_BoolArgsAreValuelessFlags guards the other half of the registry/parser
// contract: describe --llm and mbx help render an ArgSpec of type "bool"/"boolean" as a
// flag that carries no value, so every command's parser must accept it bare. A parser that
// instead consumes a following value makes the CLI's own discovery output tell callers to
// pass a flag that always fails - confirmed live against mail.flag, which registered
// --seen and --flagged as bool while requiring an explicit true/false.
func TestDefault_BoolArgsAreValuelessFlags(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	for _, op := range operations.Default.List() {
		for key, spec := range op.Args {
			if spec.Type != "bool" && spec.Type != "boolean" {
				continue
			}
			// No credentials: parsing runs to completion, then the command stops before
			// touching the network. The bare flag goes last, where a parser that wants a
			// value has none to take.
			args := append(strings.Split(op.Name, "."), "--llm", "--"+key)
			_, stderr, _ := run(t, srv, map[string]string{}, args)
			for _, reject := range []string{"requires a value", "must be true or false"} {
				if strings.Contains(stderr, reject) {
					t.Errorf("operation %q registers %q as %s but its parser demands a value: %s",
						op.Name, "--"+key, spec.Type, strings.TrimSpace(stderr))
				}
			}
		}
	}
}

// TestDefault_AcceptedDryRunIsRegistered closes the contract in the other direction. Every
// discovery surface the CLI offers - describe --llm, mbx help, usageFor - is generated from
// the registry, so a --dry-run a command accepts but does not register is invisible: "mbx
// describe mail.send --llm" listed no way to preview an irreversible send, and "mbx help
// folders.update" none to preview a permissions replacement, even though both commands
// honour the flag. A caller reading the documented arguments concludes the safe preview does
// not exist and either runs the mutation blind or does not run it at all.
func TestDefault_AcceptedDryRunIsRegistered(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	for _, op := range operations.Default.List() {
		if _, registered := op.Args["dry-run"]; registered {
			continue
		}
		// No credentials, so parsing runs to completion and the command then stops before
		// any network call. A parser that does not know the flag says so by name.
		args := append(strings.Split(op.Name, "."), "--llm", "--dry-run")
		_, stderr, _ := run(t, srv, map[string]string{}, args)
		if !strings.Contains(stderr, "unknown flag: --dry-run") {
			t.Errorf("operation %q accepts --dry-run but does not register it, so no discovery output mentions it: %s",
				op.Name, strings.TrimSpace(stderr))
		}
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("probing --dry-run made %d network requests, want 0", n)
	}
}
