// ABOUTME: Cross-command contract test for create commands: a response whose identifier
// ABOUTME: cannot be decoded must fail, never be reported as a zero or empty id.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// createResponseCase drives one create command against a server that answers with a payload
// the command cannot decode into the identifier it documents.
type createResponseCase struct {
	operation string
	args      []string
	buildArgs func(*testing.T) []string
	// module and action name the create request whose response is replaced.
	module string
	action string
	// prepare registers whatever the command reads before it creates anything.
	prepare func(*testutil.Server)
}

// TestCreateCommands_RequireAnIdentifier is the contract behind every create command's
// documented "id" output: the id is the only handle the caller has on the object it just
// created, so an undecodable, null, or missing identifier has to be an error. JSON null is
// especially important because it decodes into Go strings and integers without an error,
// leaving an empty id or zero that looks like a successful result.
func TestCreateCommands_RequireAnIdentifier(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range createResponseCases() {
		for responseName, data := range map[string]any{
			"undecodable": []any{},
			"null":        nil,
			"missing id":  map[string]any{},
		} {
			t.Run(tc.operation+"/"+responseName, func(t *testing.T) {
				srv := testutil.NewServer()
				defer srv.Close()
				srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
				if tc.prepare != nil {
					tc.prepare(srv)
				}
				srv.Handle(tc.module, tc.action, testutil.Response{
					Body: map[string]any{"data": data, "timestamp": 1},
				})

				args := append([]string{}, tc.args...)
				if tc.buildArgs != nil {
					args = tc.buildArgs(t)
				}
				stdout, stderr, code := run(t, srv, env, append(args, "--llm"))
				if code == 0 {
					t.Fatalf("exit code = 0 for %s create response; stdout=%q", responseName, stdout)
				}
				if stdout != "" {
					t.Errorf("stdout = %q, want empty on failure", stdout)
				}
				if !strings.Contains(stderr, "invalid_response") {
					t.Errorf("stderr = %q, want an invalid_response error", stderr)
				}
			})
		}
	}
}

func createResponseCases() []createResponseCase {
	return []createResponseCase{
		{
			operation: "mailfilter.create",
			args:      []string{"mailfilter", "create", "--rule", `{"rulename":"r","actioncmds":[]}`},
			module:    "/ajax/mailfilter", action: "new",
		},
		{
			operation: "snippet.create",
			args:      []string{"snippet", "create", "--displayname", "Sig", "--content", "Regards"},
			module:    "/ajax/snippet", action: "new",
		},
		{
			operation: "group.create",
			args:      []string{"group", "create", "--display-name", "Team", "--name", "team"},
			module:    "/ajax/group", action: "new",
		},
		{
			operation: "account.create",
			args: []string{
				"account", "create", "--login", "l", "--password", "p",
				"--mail-url", "imap://host:993", "--primary-address", "a@example.org",
			},
			module: "/ajax/account", action: "new",
		},
		{
			operation: "folders.create",
			args:      []string{"folders", "create", "--parent", "1", "--title", "T", "--module", "contacts"},
			module:    "/ajax/folders", action: "new",
		},
		{
			operation: "resources.create",
			args:      []string{"resources", "create", "--display-name", "Room", "--name", "room", "--mailaddress", "room@example.org"},
			module:    "/ajax/resource", action: "new",
		},
		{
			operation: "events.create",
			args: []string{
				"events", "create", "--summary", "S",
				"--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T10:00:00Z",
			},
			module: "/ajax/chronos", action: "new",
		},
		{
			operation: "tasks.create",
			args:      []string{"tasks", "create", "--title", "T", "--folder", "35"},
			module:    "/ajax/tasks", action: "new",
		},
		{
			operation: "contacts.create",
			args:      []string{"contacts", "create", "--field", "display_name=X", "--folder", "33"},
			module:    "/ajax/contacts", action: "new",
		},
		{
			operation: "reminder.create",
			args: []string{
				"reminder", "create", "--title", "T", "--folder", "35",
				"--at", "2026-07-24T09:00:00Z",
			},
			module: "/ajax/tasks", action: "new",
		},
		{
			operation: "mail.draft",
			args:      []string{"mail", "draft", "--subject", "S", "--body", "B"},
			module:    "/ajax/mail", action: "new",
		},
		{
			operation: "files.create",
			buildArgs: func(t *testing.T) []string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "upload.txt")
				if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
					t.Fatalf("writing upload fixture: %v", err)
				}
				return []string{"files", "create", "--file", path, "--folder", "36"}
			},
			module: "/ajax/infostore", action: "new",
		},
		{
			operation: "attachments.create",
			buildArgs: func(t *testing.T) []string {
				t.Helper()
				path := filepath.Join(t.TempDir(), "attachment.txt")
				if err := os.WriteFile(path, []byte("contents"), 0o600); err != nil {
					t.Fatalf("writing attachment fixture: %v", err)
				}
				return []string{
					"attachments", "create", "--module", "7", "--object-id", "4",
					"--folder", "33", "--file", path,
				}
			},
			module: "/ajax/attachment", action: "attach",
		},
	}
}
