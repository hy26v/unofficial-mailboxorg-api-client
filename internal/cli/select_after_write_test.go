// ABOUTME: Tests that --select problems are caught before a write runs, and that a write's
// ABOUTME: result is still printed (exit 5) when projection fails after the change is done.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestSelect_DottedPathIntoScalarIsRejectedBeforeTheWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "new", testutil.Response{Body: map[string]any{"data": "2"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "create", "--displayname", "Sig", "--content", "Bye", "--select", "id.x",
	})
	if code != 4 || stdout != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want invalid_argument before dispatch", code, stdout, stderr)
	}
	if !strings.Contains(stderr, "single value") {
		t.Errorf("stderr = %q, want an explanation that id has no fields", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("selector validation sent %d requests; the snippet must not be created", len(srv.Requests()))
	}
}

func TestSelect_ProjectionFailureAfterWriteKeepsTheResult(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "name"}}}},
	})
	srv.Handle("/ajax/jslob", "update", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--select", "new_value.x",
	})
	if code != exitCodeUnprojectedResult {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want %d", code, stdout, stderr, exitCodeUnprojectedResult)
	}
	if !strings.Contains(stdout, `"new_value":"name"`) || !strings.Contains(stdout, `"path":"io.ox/mail"`) {
		t.Errorf("stdout = %q, want the full unprojected result", stdout)
	}
	if !strings.Contains(stderr, `"code":"output_projection_failed"`) || !strings.Contains(stderr, "do not run the command again") {
		t.Errorf("stderr = %q, want output_projection_failed", stderr)
	}
}

func TestSelect_ProjectionFailureOnReadStaysInvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "name"}}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, _, code := run(t, srv, env, []string{
		"settings", "get", "--path", "io.ox/mail", "--key", "sort", "--select", "value.x",
	})
	if code != 4 || stdout != "" {
		t.Fatalf("exit=%d stdout=%q, want 4 with empty stdout for a read", code, stdout)
	}
}

func TestSelect_DryRunIsCheckedAgainstTheDryRunShape(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"share", "create", "--folder", "36", "--id", "4", "--dry-run", "--select", "url",
	})
	if code != 4 || stdout != "" || !strings.Contains(stderr, "dry-run fields: dry_run, folder, item") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want a dry-run field error", code, stdout, stderr)
	}

	stdout, stderr, code = run(t, srv, map[string]string{}, []string{
		"share", "create", "--folder", "36", "--id", "4", "--dry-run", "--select", "item",
	})
	if code != 0 || stdout != `{"item":"36/4"}`+"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want the dry-run item", code, stdout, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry runs sent %d requests", len(srv.Requests()))
	}
}

func TestDescribe_ListsDryRunFields(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	stdout, _, code := run(t, srv, map[string]string{}, []string{"describe", "share.create", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"dry_run_fields":["dry_run","folder","item"]`) {
		t.Fatalf("exit=%d stdout=%q, want dry_run_fields", code, stdout)
	}
}

// mail read is registered as a write because of --mark-seen. Without --mark-seen it
// changes nothing, so a --select that fails is an ordinary error and the caller may retry.
func TestSelect_ProjectionFailureOnMailReadWithoutMarkSeenStaysInvalidArgument(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--select", "from.x"})
	if code != 4 || stdout != "" || !strings.Contains(stderr, `"code":"invalid_argument"`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want invalid_argument (4) with empty stdout", code, stdout, stderr)
	}

	stdout, stderr, code = run(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--mark-seen", "--select", "from.x"})
	if code != exitCodeUnprojectedResult || !strings.Contains(stdout, `"subject":"Quarterly invoice"`) || !strings.Contains(stderr, "output_projection_failed") {
		t.Fatalf("with --mark-seen: exit=%d stdout=%q stderr=%q, want %d with the full result", code, stdout, stderr, exitCodeUnprojectedResult)
	}
}
