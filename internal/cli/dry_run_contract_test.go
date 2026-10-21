// ABOUTME: Cross-command contract test for --dry-run: a dry run asked for in human mode must
// ABOUTME: answer in human form, not with the compact JSON reserved for machine output.
package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// dryRunCase drives one --dry-run-capable operation far enough to reach its preview.
type dryRunCase struct {
	operation string
	// args are the flags, minus --dry-run, that get the command to its dry-run branch.
	args func(dir string) []string
	// handle registers whatever the command reads before it previews. Most commands preview
	// before authenticating and need nothing.
	handle func(s *testutil.Server)
}

// TestDryRun_HumanModeIsNotJSON is the output-mode half of the --dry-run contract. --llm and
// --format select machine output; without them the caller asked for the human rendering that
// every one of these commands produces for its real result. Roughly a quarter of them
// answered a human dry run with the compact JSON object instead, so "mbx files create --file
// report.pdf --dry-run" printed {"dry_run":true,"folder_id":"36",...} at a terminal while
// "mbx files create ..." without --dry-run printed an aligned table. The preview is the one
// output a cautious caller reads most carefully, and it was the one that did not match.
func TestDryRun_HumanModeIsNotJSON(t *testing.T) {
	// Every operation that registers --dry-run must appear below.
	pending := map[string]bool{}
	for _, op := range operations.Default.List() {
		if _, ok := op.Args["dry-run"]; ok {
			pending[op.Name] = true
		}
	}
	for _, tc := range dryRunCases() {
		delete(pending, tc.operation)
	}
	for name := range pending {
		t.Errorf("%s registers --dry-run but is not covered by this contract test", name)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	for _, tc := range dryRunCases() {
		t.Run(tc.operation, func(t *testing.T) {
			dir := t.TempDir()
			writeDryRunFixtures(t, dir)

			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			if tc.handle != nil {
				tc.handle(srv)
			}

			args := append(tc.args(dir), "--dry-run")
			stdout, stderr, code := run(t, srv, env, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}

			var probe any
			if err := json.Unmarshal([]byte(stdout), &probe); err == nil {
				if _, isObject := probe.(map[string]any); isObject {
					t.Errorf("human-mode dry run printed a JSON object, not human output: %s", strings.TrimSpace(stdout))
				}
			}
		})
	}
}

// TestDryRun_MachineModeStaysJSON pins the other side, so fixing the human rendering cannot
// quietly change what an agent parses.
func TestDryRun_MachineModeStaysJSON(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	for _, tc := range dryRunCases() {
		t.Run(tc.operation, func(t *testing.T) {
			dir := t.TempDir()
			writeDryRunFixtures(t, dir)

			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			if tc.handle != nil {
				tc.handle(srv)
			}

			args := append(tc.args(dir), "--dry-run", "--llm")
			stdout, stderr, code := run(t, srv, env, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}
			var probe map[string]any
			if err := json.Unmarshal([]byte(stdout), &probe); err != nil {
				t.Errorf("--llm dry run is not a JSON object: %s", strings.TrimSpace(stdout))
			}
		})
	}
}

func writeDryRunFixtures(t *testing.T, dir string) {
	t.Helper()
	for name, contents := range map[string]string{
		"payload.txt":   "hello",
		"contacts.vcf":  "BEGIN:VCARD\nVERSION:3.0\nFN:A B\nEND:VCARD\n",
		"calendar.ics":  "BEGIN:VCALENDAR\nVERSION:2.0\nEND:VCALENDAR\n",
		"requests.json": `[{"resource":"mail","action":"all","params":{"folder":"default0/INBOX"}}]`,
		"moves.json":    `[{"folder":"default0/INBOX","id":"12","to":"default0/Archive"}]`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
			t.Fatalf("writing fixture %s: %v", name, err)
		}
	}
}

// tasksFolderServer answers the standard-tasks-folder discovery the tasks write commands do
// before they preview.
func tasksFolderServer(s *testutil.Server) {
	s.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
	})
}

func dryRunCases() []dryRunCase {
	fixed := func(args ...string) func(string) []string {
		return func(string) []string { return args }
	}
	return []dryRunCase{
		{operation: "account.create", args: fixed("account", "create", "--login", "u", "--password", "p", "--mail-url", "imap://h:993", "--primary-address", "u@example.org")},
		{operation: "account.update", args: fixed("account", "update", "--id", "3", "--name", "New")},
		{operation: "account.delete", args: fixed("account", "delete", "--id", "3", "--confirm", "3")},
		{
			operation: "attachments.create",
			args: func(dir string) []string {
				return []string{"attachments", "create", "--module", "7", "--object-id", "5", "--folder", "33", "--file", filepath.Join(dir, "payload.txt")}
			},
		},
		{operation: "attachments.delete", args: fixed("attachments", "delete", "--module", "7", "--object-id", "5", "--folder", "33", "--id", "9", "--confirm", "9")},
		{
			operation: "batch.run",
			args: func(dir string) []string {
				return []string{"batch", "run", "--input", "@" + filepath.Join(dir, "requests.json")}
			},
		},
		{operation: "change-password", args: fixed("change-password", "--current-password", "old", "--new-password", "new", "--confirm", "fixture-user@example.invalid")},
		{operation: "contacts.create", args: fixed("contacts", "create", "--field", "display_name=A B")},
		{operation: "contacts.update", args: fixed("contacts", "update", "--id", "5", "--timestamp", "1", "--field", "display_name=A B")},
		{operation: "contacts.delete", args: fixed("contacts", "delete", "--id", "5", "--timestamp", "1", "--confirm", "5")},
		{
			operation: "contacts.import",
			args: func(dir string) []string {
				return []string{"contacts", "import", "--file", filepath.Join(dir, "contacts.vcf")}
			},
		},
		{operation: "events.create", args: fixed("events", "create", "--summary", "Standup", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T09:30:00Z")},
		{operation: "events.update", args: fixed("events", "update", "--id", "77", "--summary", "Standup")},
		{operation: "events.delete", args: fixed("events", "delete", "--id", "77", "--confirm", "77")},
		{
			operation: "events.import",
			args: func(dir string) []string {
				return []string{"events", "import", "--file", filepath.Join(dir, "calendar.ics")}
			},
		},
		{
			operation: "files.create",
			args: func(dir string) []string {
				return []string{"files", "create", "--file", filepath.Join(dir, "payload.txt")}
			},
		},
		{operation: "files.update", args: fixed("files", "update", "--id", "36/2", "--title", "New")},
		{operation: "files.delete", args: fixed("files", "delete", "--id", "36/2", "--confirm", "36/2")},
		{operation: "folders.create", args: fixed("folders", "create", "--parent", "1", "--title", "Work", "--module", "contacts")},
		{operation: "folders.ensure", args: fixed("folders", "ensure", "--parent", "1", "--title", "Work", "--module", "contacts")},
		{operation: "folders.update", args: fixed("folders", "update", "--id", "33/17", "--title", "Work")},
		{operation: "folders.delete", args: fixed("folders", "delete", "--id", "33/17", "--confirm", "33/17")},
		{operation: "group.create", args: fixed("group", "create", "--display-name", "Team", "--name", "team")},
		{operation: "group.update", args: fixed("group", "update", "--id", "3", "--display-name", "Team")},
		{operation: "group.delete", args: fixed("group", "delete", "--id", "3", "--confirm", "3")},
		{operation: "login", args: fixed("login")},
		{operation: "logout", args: fixed("logout")},
		{operation: "mail.move", args: fixed("mail", "move", "--id", "12", "--folder", "default0/INBOX", "--to", "default0/Archive")},
		{
			operation: "mail.move-many",
			args: func(dir string) []string {
				return []string{"mail", "move-many", "--input", "@" + filepath.Join(dir, "moves.json")}
			},
		},
		{operation: "mail.copy", args: fixed("mail", "copy", "--id", "12", "--folder", "default0/INBOX", "--to", "default0/Archive")},
		{operation: "mail.flag", args: fixed("mail", "flag", "--id", "12", "--folder", "default0/INBOX", "--seen", "true")},
		{operation: "mail.delete", args: fixed("mail", "delete", "--id", "12", "--folder", "default0/INBOX", "--confirm", "12")},
		{operation: "mail.send", args: fixed("mail", "send", "--to", "fixture-user@example.invalid", "--subject", "Hi", "--body", "Hello")},
		{operation: "mail.draft", args: fixed("mail", "draft", "--to", "fixture-user@example.invalid", "--subject", "Hi", "--body", "Hello")},
		{operation: "mailfilter.create", args: fixed("mailfilter", "create", "--rule", `{"rulename":"r","actioncmds":[]}`)},
		{operation: "mailfilter.update", args: fixed("mailfilter", "update", "--id", "7", "--rule", `{"active":false}`)},
		{operation: "mailfilter.delete", args: fixed("mailfilter", "delete", "--id", "7", "--confirm", "7")},
		{operation: "reminder.create", args: fixed("reminder", "create", "--title", "Ping", "--folder", "35", "--at", "2026-07-24T09:00:00Z")},
		{operation: "reminder.delete", args: fixed("reminder", "delete", "--id", "9", "--confirm", "9")},
		{operation: "resources.create", args: fixed("resources", "create", "--display-name", "Room", "--name", "room", "--mailaddress", "room@example.org")},
		{operation: "resources.update", args: fixed("resources", "update", "--id", "3", "--display-name", "Room")},
		{operation: "resources.delete", args: fixed("resources", "delete", "--id", "3", "--confirm", "3")},
		{
			operation: "settings.set",
			args:      fixed("settings", "set", "--path", "io.ox/mail", "--key", "threadView", "--value", "off"),
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/jslob", "list", testutil.Response{
					Body: map[string]any{"data": []any{map[string]any{"id": "io.ox/mail", "tree": map[string]any{"threadView": "on"}}}},
				})
			},
		},
		{operation: "share.create", args: fixed("share", "create", "--folder", "36", "--id", "2")},
		{operation: "share.delete", args: fixed("share", "delete", "--folder", "36", "--id", "2", "--confirm", "2")},
		{operation: "snippet.create", args: fixed("snippet", "create", "--displayname", "Sig", "--content", "Bye")},
		{operation: "snippet.update", args: fixed("snippet", "update", "--id", "2", "--content", "Bye")},
		{operation: "snippet.delete", args: fixed("snippet", "delete", "--id", "2", "--confirm", "2")},
		{operation: "tasks.create", args: fixed("tasks", "create", "--title", "Buy milk"), handle: tasksFolderServer},
		{operation: "tasks.update", args: fixed("tasks", "update", "--id", "4", "--timestamp", "1", "--title", "Buy milk"), handle: tasksFolderServer},
		{operation: "tasks.delete", args: fixed("tasks", "delete", "--id", "4", "--timestamp", "1", "--confirm", "4")},
		{
			operation: "user.update",
			args:      fixed("user", "update", "--display-name", "CLI Test"),
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/user", "get", testutil.Response{
					Body: map[string]any{
						"data":      map[string]any{"id": 42, "display_name": "Old", "timezone": "Europe/Berlin"},
						"timestamp": 1,
					},
				})
			},
		},
	}
}
