// ABOUTME: Integration tests for API response validation across auth, mail, contacts,
// ABOUTME: tasks, events, settings, and transport response handling.
package cli

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestHTTPFailureDoesNotLogOutLocally(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "logout", testutil.Response{
		Status: http.StatusServiceUnavailable,
		Body:   map[string]any{"data": nil},
	})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{ID: "sess-1"}); err != nil {
		t.Fatalf("priming session store: %v", err)
	}

	stdout, stderr, code := runWithOutput(t, srv, sessionPath, func(string) string { return "" }, []string{"logout", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "http_error") {
		t.Errorf("stderr = %q, want http_error", stderr)
	}
	sess, err := store.Load()
	if err != nil {
		t.Fatalf("loading session after failed logout: %v", err)
	}
	if sess.ID != "sess-1" {
		t.Errorf("session id = %q, want retained after failed logout", sess.ID)
	}
}

func TestLoginResponseRequiresSessionID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"login", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailSendResponseRequiresMessageID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "new", testutil.Response{
		Body: []byte(`<script>(parent["callback_new"])({"data":null})</script>`),
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "send", "--to", "recipient@example.com", "--subject", "hello", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailMoveReportsServerAssignedID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "default0/Archive", "id": "99"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"id":"99"`) {
		t.Errorf("stdout = %q, want server-assigned id 99", stdout)
	}
}

func TestMailCopyResponseRequiresNewID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "copy", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "default0/Archive"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"mail", "copy", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailInputRejectsUnknownFields(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	input := `{"to":["recipient@example.com"],"subject":"hello","bccs":["hidden@example.com"]}`
	stdout, stderr, code := run(t, srv, map[string]string{},
		[]string{"mail", "send", "--input", input, "--dry-run", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "bccs") {
		t.Errorf("stderr = %q, want unknown field name", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d network requests, want 0", len(srv.Requests()))
	}
}

func TestContactsListRejectsMalformedRows(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "6"}},
	})
	srv.Handle("/ajax/contacts", "all", testutil.Response{
		Body: map[string]any{"data": [][]any{{"2", "Incomplete contact"}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
}

func TestContactsListRejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "6"}},
	})
	srv.Handle("/ajax/contacts", "all", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksListRejectsMalformedRows(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{"data": [][]any{{4, "Incomplete task"}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--folder", "35", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
}

func TestEventsDeleteReportsNestedResultError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 2000},
	})
	srv.Handle("/ajax/chronos", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"folderId": "cal://0/32",
			"id":       "evt-1",
			"error": map[string]any{
				"error":      "event changed since it was read",
				"code":       "CAL-4020",
				"categories": "TRY_AGAIN",
				"error_id":   "req-delete-1",
			},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "CAL-4020") || !strings.Contains(stderr, `"retryable":true`) {
		t.Errorf("stderr = %q, want retryable CAL-4020 result error", stderr)
	}
}

func TestEventsDeleteAcceptsSuccessfulCalendarResults(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 2000},
	})
	srv.Handle("/ajax/chronos", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"folderId": "cal://0/32",
			"id":       "evt-1",
			"deleted":  []any{map[string]any{"id": "evt-1", "folder": "cal://0/32"}},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want deleted:true", stdout)
	}
}

func TestSettingsGetMissingKeyIsNotFound(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread"}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env,
		[]string{"settings", "get", "--path", "io.ox/mail", "--key", "missing", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "not_found") {
		t.Errorf("stderr = %q, want not_found", stderr)
	}
}
