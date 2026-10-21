// ABOUTME: Unit tests for signIn, the one sign-in helper of the command handlers.
// ABOUTME: Pins the error code and exit code for each kind of sign-in failure.
package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func signInForTest(t *testing.T, srv *testutil.Server, env map[string]string, sessionPath string) (*cliErrors.Error, string) {
	t.Helper()
	if sessionPath == "" {
		sessionPath = filepath.Join(t.TempDir(), "session.json")
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, err := signIn(ctx, srv.Client(), srv.URL+"/ajax", sessionPath, func(k string) string { return env[k] })
	if err != nil {
		if c != nil {
			t.Error("signIn returned a client together with an error")
		}
		return err, ""
	}
	if c == nil {
		t.Fatal("signIn returned no client and no error")
	}
	return nil, sess.ID
}

var signInTestCredentials = map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

func TestSignIn_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	err, sessionID := signInForTest(t, srv, signInTestCredentials, "")
	if err != nil {
		t.Fatalf("unexpected error: %s %s", err.Code, err.Message)
	}
	if sessionID != "sess-1" {
		t.Errorf("session id = %q, want sess-1", sessionID)
	}
}

func TestSignIn_MissingCredentialsIsInvalidArgumentWithoutRequests(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	err, _ := signInForTest(t, srv, map[string]string{}, "")
	if err == nil || err.Code != "invalid_argument" || cliErrors.ExitCodeFor(err) != 4 {
		t.Fatalf("error = %+v, want invalid_argument with exit code 4", err)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("sent %d requests without credentials, want none", n)
	}
}

func TestSignIn_RateLimitKeepsItsStatusAndRetryData(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Status: http.StatusTooManyRequests, Body: map[string]any{}})

	err, _ := signInForTest(t, srv, signInTestCredentials, "")
	if err == nil {
		t.Fatal("rate-limited sign-in returned no error")
	}
	if err.Code == "auth_failed" || err.HTTPStatus != http.StatusTooManyRequests || !err.Retryable {
		t.Errorf("error = %+v, want the retryable 429 error, not auth_failed", err)
	}
	if code := cliErrors.ExitCodeFor(err); code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
}

func TestSignIn_RejectedLoginKeepsTheServerCode(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT"},
	})

	err, _ := signInForTest(t, srv, signInTestCredentials, "")
	if err == nil || err.Code != "LGI-0006" || cliErrors.ExitCodeFor(err) != 2 {
		t.Fatalf("error = %+v, want LGI-0006 with exit code 2", err)
	}
}

func TestSignIn_ServerErrorIsNotAuthFailed(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Status: http.StatusBadGateway, Body: map[string]any{}})

	err, _ := signInForTest(t, srv, signInTestCredentials, "")
	if err == nil || err.Code == "auth_failed" || err.HTTPStatus != http.StatusBadGateway {
		t.Fatalf("error = %+v, want the 502 error, not auth_failed", err)
	}
}

func TestSignIn_UnstructuredErrorFallsBackToAuthFailed(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	if err := os.WriteFile(sessionPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	err, _ := signInForTest(t, srv, signInTestCredentials, sessionPath)
	if err == nil || err.Code != "auth_failed" || cliErrors.ExitCodeFor(err) != 2 {
		t.Fatalf("error = %+v, want auth_failed with exit code 2 for an unreadable session file", err)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("sent %d requests with an unreadable session file, want none", n)
	}
}

func TestSignIn_TransportFailureIsRetryableNetworkError(t *testing.T) {
	closed := httptest.NewServer(http.NotFoundHandler())
	baseURL := closed.URL + "/ajax"
	closed.Close()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	_, _, err := signIn(ctx, http.DefaultClient, baseURL, sessionPath, func(k string) string { return signInTestCredentials[k] })
	if err == nil || err.Code != "network_error" || !err.Retryable || cliErrors.ExitCodeFor(err) != 1 {
		t.Fatalf("error = %+v, want retryable network_error with exit code 1 for a refused connection", err)
	}
}

// signInCommands lists, for each command that signs in with signIn, an argument list that
// passes the local checks and so reaches sign-in. Add each command that moves to signIn.
//
// contacts import checks before sign-in that --file is a regular file and reads it only
// after sign-in, so its entry names this test file, which is in the working directory.
func signInCommands() map[string][]string {
	return map[string][]string{
		"whoami":           {"whoami", "--llm"},
		"quota get":        {"quota", "get", "--type", "filestore", "--llm"},
		"snippet list":     {"snippet", "list", "--llm"},
		"snippet get":      {"snippet", "get", "--id", "2", "--llm"},
		"snippet create":   {"snippet", "create", "--displayname", "d", "--content", "c", "--llm"},
		"snippet update":   {"snippet", "update", "--id", "2", "--content", "c", "--llm"},
		"snippet delete":   {"snippet", "delete", "--id", "2", "--confirm", "2", "--llm"},
		"settings get":     {"settings", "get", "--path", "io.ox/core", "--llm"},
		"settings set":     {"settings", "set", "--path", "io.ox/core", "--key", "k", "--value", "1", "--llm"},
		"user update":      {"user", "update", "--display-name", "n", "--llm"},
		"contacts create":  {"contacts", "create", "--field", "display_name=A", "--llm"},
		"contacts delete":  {"contacts", "delete", "--id", "5", "--timestamp", "1", "--confirm", "5", "--llm"},
		"contacts export":  {"contacts", "export", "--output", os.DevNull, "--llm"},
		"contacts get":     {"contacts", "get", "--id", "5", "--llm"},
		"contacts import":  {"contacts", "import", "--file", "session_auth_test.go", "--llm"},
		"contacts list":    {"contacts", "list", "--llm"},
		"contacts update":  {"contacts", "update", "--id", "5", "--timestamp", "1", "--field", "display_name=A", "--llm"},
		"folders create":   {"folders", "create", "--parent", "1", "--title", "T", "--module", "contacts", "--llm"},
		"folders delete":   {"folders", "delete", "--id", "5", "--confirm", "5", "--llm"},
		"folders ensure":   {"folders", "ensure", "--parent", "1", "--title", "T", "--module", "contacts", "--llm"},
		"folders get":      {"folders", "get", "--id", "1", "--llm"},
		"folders list":     {"folders", "list", "--llm"},
		"folders update":   {"folders", "update", "--id", "5", "--title", "T", "--llm"},
		"files list":       {"files", "list", "--folder", "36", "--llm"},
		"attachments list": {"attachments", "list", "--module", "7", "--object-id", "4", "--folder", "33", "--llm"},
		"mail read":        {"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"},

		// events, reminder and tasks. Sign-in fails, so export writes no --output file and
		// import (which only needs an existing regular file) reads nothing.
		"events create":   {"events", "create", "--summary", "s", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T10:00:00Z", "--llm"},
		"events delete":   {"events", "delete", "--id", "5", "--confirm", "5", "--llm"},
		"events export":   {"events", "export", "--output", "never-written.ics", "--llm"},
		"events get":      {"events", "get", "--id", "5", "--llm"},
		"events import":   {"events", "import", "--file", "session_auth_test.go", "--llm"},
		"events list":     {"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm"},
		"events update":   {"events", "update", "--id", "5", "--summary", "s", "--llm"},
		"reminder create": {"reminder", "create", "--title", "t", "--folder", "30", "--at", "2026-07-24T09:00:00Z", "--llm"},
		"reminder delete": {"reminder", "delete", "--id", "5", "--confirm", "5", "--llm"},
		"reminder list":   {"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm"},
		"tasks create":    {"tasks", "create", "--title", "t", "--llm"},
		"tasks delete":    {"tasks", "delete", "--id", "5", "--timestamp", "1", "--confirm", "5", "--llm"},
		"tasks get":       {"tasks", "get", "--id", "5", "--llm"},
		"tasks list":      {"tasks", "list", "--llm"},
		"tasks update":    {"tasks", "update", "--id", "5", "--timestamp", "1", "--title", "t", "--llm"},
		// group, resources, share, capabilities and batch.
		"batch run":         {"batch", "run", "--input", `[{"operation":"whoami"}]`, "--llm"},
		"capabilities list": {"capabilities", "list", "--llm"},
		"group create":      {"group", "create", "--display-name", "d", "--name", "n", "--llm"},
		"group delete":      {"group", "delete", "--id", "3", "--confirm", "3", "--llm"},
		"group get":         {"group", "get", "--id", "3", "--llm"},
		"group list":        {"group", "list", "--llm"},
		"group update":      {"group", "update", "--id", "3", "--name", "n", "--llm"},
		"resources create":  {"resources", "create", "--display-name", "d", "--name", "n", "--mailaddress", "room@example.invalid", "--llm"},
		"resources delete":  {"resources", "delete", "--id", "3", "--confirm", "3", "--llm"},
		"resources search":  {"resources", "search", "--llm"},
		"resources update":  {"resources", "update", "--id", "3", "--name", "n", "--llm"},
		"share create":      {"share", "create", "--folder", "36", "--id", "4", "--llm"},
		"share delete":      {"share", "delete", "--folder", "36", "--id", "4", "--confirm", "4", "--llm"},
		// Mail and attachments commands moved to parseCommandFlags.
		"attachments create": {"attachments", "create", "--module", "7", "--object-id", "4", "--folder", "33", "--file", "attachments.go", "--llm"},
		"attachments delete": {"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "5", "--confirm", "5", "--llm"},
		"attachments get":    {"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "5", "--llm"},
		"mail attachment":    {"mail", "attachment", "--id", "7", "--folder", "default0/INBOX", "--attachment", "2", "--save-to", "unused-attachment", "--llm"},
		"mail copy":          {"mail", "copy", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive", "--llm"},
		"mail delete":        {"mail", "delete", "--id", "7", "--folder", "default0/INBOX", "--confirm", "7", "--llm"},
		"mail draft":         {"mail", "draft", "--to", "a@example.invalid", "--subject", "s", "--llm"},
		"mail flag":          {"mail", "flag", "--id", "7", "--folder", "default0/INBOX", "--seen", "true", "--llm"},
		"mail get":           {"mail", "get", "--id", "7", "--folder", "default0/INBOX", "--llm"},
		"mail list":          {"mail", "list", "--llm"},
		"mail move":          {"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive", "--llm"},
		"mail move-many":     {"mail", "move-many", "--input", `[{"folder":"default0/INBOX","id":"7","to":"default0/Archive"}]`, "--preflight", "--llm"},
		"mail search":        {"mail", "search", "--query", "invoice", "--llm"},
		"mail send":          {"mail", "send", "--to", "a@example.invalid", "--subject", "s", "--llm"},
		"account create":     {"account", "create", "--login", "l", "--password", "p", "--mail-url", "imap://host:993", "--primary-address", "a@example.invalid", "--llm"},
		"account delete":     {"account", "delete", "--id", "5", "--confirm", "5", "--llm"},
		"account get":        {"account", "get", "--id", "5", "--llm"},
		"account list":       {"account", "list", "--llm"},
		"account update":     {"account", "update", "--id", "5", "--name", "n", "--llm"},
		"files create":       {"files", "create", "--file", "testdata/commands_llm.golden", "--folder", "36", "--llm"},
		"files delete":       {"files", "delete", "--id", "36/2", "--confirm", "36/2", "--llm"},
		"files get":          {"files", "get", "--id", "36/2", "--llm"},
		"files update":       {"files", "update", "--id", "36/2", "--title", "t", "--llm"},
		"mailfilter create":  {"mailfilter", "create", "--rule", `{"rulename":"r","actioncmds":[]}`, "--llm"},
		"mailfilter delete":  {"mailfilter", "delete", "--id", "7", "--confirm", "7", "--llm"},
		"mailfilter get":     {"mailfilter", "get", "--id", "7", "--llm"},
		"mailfilter list":    {"mailfilter", "list", "--llm"},
		"mailfilter update":  {"mailfilter", "update", "--id", "7", "--rule", `{"active":false}`, "--llm"},
	}
}

func TestSignInCommands_ReportTheRealSignInError(t *testing.T) {
	for name, args := range signInCommands() {
		t.Run(name+"/missing credentials", func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code != 4 || stdout != "" || !strings.Contains(stderr, `"invalid_argument"`) {
				t.Errorf("code=%d stdout=%q stderr=%q, want invalid_argument with exit code 4", code, stdout, stderr)
			}
			if n := len(srv.Requests()); n != 0 {
				t.Errorf("sent %d requests without credentials, want none", n)
			}
		})
		t.Run(name+"/rate limit", func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Status: http.StatusTooManyRequests, Body: map[string]any{}})
			stdout, stderr, code := run(t, srv, signInTestCredentials, args)
			if code != 1 || stdout != "" || !strings.Contains(stderr, `"http_status":429`) || !strings.Contains(stderr, `"retryable":true`) {
				t.Errorf("code=%d stdout=%q stderr=%q, want the retryable 429 error with exit code 1", code, stdout, stderr)
			}
			if strings.Count(stderr, `"error"`) != 1 {
				t.Errorf("stderr = %q, want exactly one error document", stderr)
			}
		})
		t.Run(name+"/rejected login", func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{
				Body: map[string]any{"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT"},
			})
			_, stderr, code := run(t, srv, signInTestCredentials, args)
			if code != 2 || !strings.Contains(stderr, "LGI-0006") || strings.Count(stderr, `"error"`) != 1 {
				t.Errorf("code=%d stderr=%q, want one LGI-0006 error with exit code 2", code, stderr)
			}
		})
	}
}
