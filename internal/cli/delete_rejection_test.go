// ABOUTME: Contract tests for the OX delete contract: action=delete answers with the objects
// ABOUTME: it refused to delete, so a non-empty answer must not be reported as a deletion.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// The OX middleware's action=delete does not fail the request when it declines to delete an
// object. It answers 200 with an array naming the objects it refused, which is how a stale
// --timestamp (someone else changed the object since it was read) reaches the caller. A
// command that ignores that array prints "deleted: true" for an object that is still there,
// which is the single most misleading thing a destructive command can do. files.delete and
// mail.delete already check it; these are the commands that did not.

func TestContactsDelete_ServerRefusedTheDelete_IsAnError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": "5", "folder": "33"}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "delete", "--id", "5", "--folder", "33", "--timestamp", "1", "--confirm", "5", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want no deletion confirmation for a refused delete", stdout)
	}
	if !strings.Contains(stderr, "not deleted") {
		t.Errorf("stderr = %q, want an explanation that the contact was not deleted", stderr)
	}
}

func TestTasksDelete_ServerRefusedTheDelete_IsAnError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
	})
	srv.Handle("/ajax/tasks", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": 4, "folder": "35"}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "delete", "--id", "4", "--timestamp", "123", "--confirm", "4", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want no deletion confirmation for a refused delete", stdout)
	}
	if !strings.Contains(stderr, "not deleted") {
		t.Errorf("stderr = %q, want an explanation that the task was not deleted", stderr)
	}
}

func TestFoldersDelete_ServerRefusedTheDelete_IsAnError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})
	srv.Handle("/ajax/folders", "delete", testutil.Response{
		Body: map[string]any{"data": []any{"33/17"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "delete", "--id", "33/17", "--confirm", "33/17", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if strings.Contains(stdout, "33/17") {
		t.Errorf("stdout = %q, want no deletion confirmation for a refused delete", stdout)
	}
	if !strings.Contains(stderr, "not deleted") {
		t.Errorf("stderr = %q, want an explanation that the folder was not deleted", stderr)
	}
}

func TestResourcesDelete_ServerRefusedTheDelete_IsAnError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 5, "last_modified_utc": 1700000000000}},
	})
	srv.Handle("/ajax/resource", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": 5}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "delete", "--id", "5", "--confirm", "5", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want no deletion confirmation for a refused delete", stdout)
	}
	if !strings.Contains(stderr, "not deleted") {
		t.Errorf("stderr = %q, want an explanation that the resource was not deleted", stderr)
	}
}

func TestGroupDelete_ServerRefusedTheDelete_IsAnError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 5, "last_modified_utc": 1700000000000}},
	})
	srv.Handle("/ajax/group", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": 5}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "delete", "--id", "5", "--confirm", "5", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want no deletion confirmation for a refused delete", stdout)
	}
	if !strings.Contains(stderr, "not deleted") {
		t.Errorf("stderr = %q, want an explanation that the group was not deleted", stderr)
	}
}

func TestAccountDelete_NonIDResult_IsAnError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": 5}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"account", "delete", "--id", "5", "--confirm", "5", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want no deletion confirmation for a refused delete", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") || !strings.Contains(stderr, "did not confirm") {
		t.Errorf("stderr = %q, want an unconfirmed account-delete response error", stderr)
	}
}

// An empty array is the success answer and must stay one, including the "data": {} shape the
// deployment returns for some modules, which is not an array at all.
func TestDelete_EmptyRefusalList_StaysASuccess(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "delete", testutil.Response{
		Body: map[string]any{"data": []any{}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "delete", "--id", "5", "--folder", "33", "--timestamp", "1", "--confirm", "5", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want a deletion confirmation", stdout)
	}
}
