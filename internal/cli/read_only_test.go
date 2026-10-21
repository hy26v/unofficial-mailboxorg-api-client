// ABOUTME: Tests for read-only mode (safety.read_only and MBX_READ_ONLY).
// ABOUTME: Write commands and write flags are refused before any request; reads still run.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

const readOnlyConfig = "[safety]\nread_only = true\n"

func readOnlyEnvWith(value string) map[string]string {
	return map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw", "MBX_READ_ONLY": value}
}

func assertReadOnlyRefusal(t *testing.T, srv *testutil.Server, stdout, stderr string, code int, wantInMessage string) {
	t.Helper()
	if code != 1 || !strings.Contains(stderr, `"code":"read_only"`) {
		t.Fatalf("code=%d stderr=%q; want read_only with exit code 1", code, stderr)
	}
	if !strings.Contains(stderr, wantInMessage) {
		t.Errorf("stderr = %q, want it to mention %q", stderr, wantInMessage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("sent %d requests in read-only mode, want none", n)
	}
}

func TestReadOnly_ConfigRefusesWriteBeforeAnyRequest(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(t, srv, mailReadEnv, []string{"snippet", "create", "--displayname", "d", "--content", "c", "--llm"}, readOnlyConfig)
	assertReadOnlyRefusal(t, srv, stdout, stderr, code, "snippet.create changes account data")
}

func TestReadOnly_EnvRefusesDestructiveCommand(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, readOnlyEnvWith("1"), []string{"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "7", "--llm"})
	assertReadOnlyRefusal(t, srv, stdout, stderr, code, "MBX_READ_ONLY")
}

func TestReadOnly_RefusesMailReadMarkSeen(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, readOnlyEnvWith("1"), []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--mark-seen", "--llm"})
	assertReadOnlyRefusal(t, srv, stdout, stderr, code, "--mark-seen of mail.read")
}

func TestReadOnly_AllowsMailReadWithoutMarkSeen(t *testing.T) {
	srv := newMailReadServer(t, mailReadMessage(plainBodyPart))
	defer srv.Close()

	stdout, stderr, code := runWithConfig(t, srv, mailReadEnv, []string{"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"}, readOnlyConfig)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q; want a normal read in read-only mode", code, stderr)
	}
	if !strings.Contains(stdout, "your invoice is attached") {
		t.Errorf("stdout = %q, want the message body", stdout)
	}
}

func TestReadOnly_AllowsDryRunOfAWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(t, srv, mailReadEnv, []string{"snippet", "create", "--displayname", "d", "--content", "c", "--dry-run", "--llm"}, readOnlyConfig)
	if code != 0 || !strings.Contains(stdout, `"dry_run":true`) {
		t.Fatalf("code=%d stdout=%q stderr=%q; want the dry run to work in read-only mode", code, stdout, stderr)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("dry run sent %d requests, want none", n)
	}
}

func TestReadOnly_AllowsLogin(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	_, stderr, code := runWithConfig(t, srv, mailReadEnv, []string{"login", "--llm"}, readOnlyConfig)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q; want login to work in read-only mode", code, stderr)
	}
}

func TestReadOnly_EnvCannotTurnOffConfiguredMode(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfig(t, srv, readOnlyEnvWith("0"), []string{"snippet", "delete", "--id", "2", "--confirm", "2", "--llm"}, readOnlyConfig)
	assertReadOnlyRefusal(t, srv, stdout, stderr, code, "safety.read_only")
}

func TestReadOnly_UnknownEnvValueFailsClosed(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	_, stderr, code := run(t, srv, readOnlyEnvWith("maybe"), []string{"whoami", "--llm"})
	if code != 1 || !strings.Contains(stderr, `"code":"invalid_configuration"`) || !strings.Contains(stderr, "MBX_READ_ONLY") {
		t.Fatalf("code=%d stderr=%q; want invalid_configuration naming MBX_READ_ONLY", code, stderr)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("sent %d requests with an invalid MBX_READ_ONLY, want none", n)
	}
}

func TestReadOnly_OffByDefault(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "new", testutil.Response{Body: map[string]any{"data": "5"}})

	_, stderr, code := run(t, srv, readOnlyEnvWith(""), []string{"snippet", "create", "--displayname", "d", "--content", "c", "--llm"})
	if strings.Contains(stderr, "read_only") {
		t.Fatalf("code=%d stderr=%q; read-only mode must be off unless turned on", code, stderr)
	}
}

// Every registered operation that changes data, apart from the session commands, must be
// refused, so a new write command cannot slip past read-only mode.
func TestReadOnly_RefusesEveryRegisteredWrite(t *testing.T) {
	getenv := func(string) string { return "" }
	for _, op := range operations.Default.List() {
		args := strings.SplitN(op.Name, ".", 2)
		err := commandReadOnlyError(args, true, getenv)
		switch {
		case readOnlySessionOperations[op.Name]:
			if err != nil {
				t.Errorf("%s: refused a session command: %v", op.Name, err.Message)
			}
		case op.SideEffect != operations.SideEffectRead:
			if err == nil || err.Code != "read_only" {
				t.Errorf("%s (%s): not refused in read-only mode", op.Name, op.SideEffect)
			}
		default:
			if err != nil {
				t.Errorf("%s: read refused in read-only mode: %v", op.Name, err.Message)
			}
		}
	}
}

func TestReadOnly_HelpTopicAndFlagEffectInHelp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, _, code := run(t, srv, map[string]string{}, []string{"help", "read-only"})
	if code != 0 || !strings.Contains(stdout, "read_only = true") || !strings.Contains(stdout, "MBX_READ_ONLY=1") {
		t.Errorf("help read-only: code=%d stdout=%q", code, stdout)
	}
	stdout, _, code = run(t, srv, map[string]string{}, []string{"help", "mail.read"})
	if code != 0 || !strings.Contains(stdout, "bool, optional, effect write") {
		t.Errorf("help mail.read: code=%d stdout=%q; want --mark-seen marked effect write", code, stdout)
	}
}
