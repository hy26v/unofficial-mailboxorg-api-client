// ABOUTME: CLI integration tests for the folders list command: human and LLM output,
// ABOUTME: parent handling, authentication failures, API failures, and stdout/stderr/exit-code separation.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func requireFolderRequestPayload(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	folder, ok := body["folder"].(map[string]any)
	if !ok {
		t.Fatalf("request body = %v, want top-level folder object", body)
	}
	return folder
}

func TestFoldersList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"32", "Calendar", "calendar", 1, false},
			[]any{"default0/INBOX", "Inbox", "mail", 7, false},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Calendar") || !strings.Contains(stdout, "Inbox") {
		t.Errorf("stdout = %q, want folder titles present", stdout)
	}
}

func TestFoldersList_HumanMode_EscapesControlCharacters(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"32", "Calendar\tShared\nInjected", "calendar", 1, false},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `Calendar\tShared\nInjected`) {
		t.Errorf("stdout = %q, want control characters escaped inside one cell", stdout)
	}
	if got := strings.Count(stdout, "\n"); got != 2 {
		t.Errorf("stdout has %d lines, want one header and one folder row: %q", got, stdout)
	}
}

func TestFoldersList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"32", "Calendar", "calendar", 1, false},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	want := `{"folders":[{"id":"32","title":"Calendar","module":"calendar","type":1,"has_subfolders":false}]}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestFoldersList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFoldersList_UsesParentFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"folders", "list", "--parent", "9", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var gotParent string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "list" {
			gotParent = req.URL.Query().Get("parent")
		}
	}
	if gotParent != "9" {
		t.Errorf("parent query param = %q, want %q", gotParent, "9")
	}
}

func TestFoldersList_DefaultsParentToRoot(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"folders", "list", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var gotParent string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "list" {
			gotParent = req.URL.Query().Get("parent")
		}
	}
	if gotParent != "1" {
		t.Errorf("default parent query param = %q, want %q", gotParent, "1")
	}
}

func TestFoldersList_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"folders", "list", "--bogus", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--bogus") {
		t.Errorf("stderr = %q, want mention of unknown flag", stderr)
	}
}

func TestFoldersList_MissingCredentials_AuthFailedError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"folders", "list", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "error") {
		t.Errorf("stderr = %q, want structured error", stderr)
	}
}

func TestFoldersList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{
			"error": "Such a folder does not exist.", "error_params": []any{"999", "2044270"},
			"categories": "ERROR", "category": 8, "code": "FLD-0008", "error_id": "req-1",
			"error_desc": "Folder 999 does not exist in context 2044270",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "list", "--parent", "999", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "FLD-0008") {
		t.Errorf("stderr = %q, want FLD-0008 code present", stderr)
	}
}

func TestFoldersGet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "32", "title": "Calendar", "module": "calendar", "folder_id": "1",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "get", "--id", "32", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"32","title":"Calendar","module":"calendar","parent_id":"1","has_subfolders":false,"last_modified":1700000000000,"permissions":null}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotID string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "get" {
			gotID = req.URL.Query().Get("id")
		}
	}
	if gotID != "32" {
		t.Errorf("id query param = %q, want %q", gotID, "32")
	}
}

func TestFoldersGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33", "title": "Contacts", "module": "contacts", "folder_id": "1",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "get", "--id", "32", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFoldersGet_RejectsMissingTitle(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "32", "module": "calendar", "folder_id": "1",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "get", "--id", "32", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing title rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFoldersGet_RejectsMissingModule(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "32", "title": "Calendar", "folder_id": "1",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "get", "--id", "32", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing module rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFoldersGet_RejectsMissingParentID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "32", "title": "Calendar", "module": "calendar",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "get", "--id", "32", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing parent id rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFoldersGet_RejectsMissingSubfolderFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "32", "title": "Calendar", "module": "calendar", "folder_id": "1",
			"last_modified": 1700000000000,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "get", "--id", "32", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing subfolder flag rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFoldersGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"folders", "get", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--id") {
		t.Errorf("stderr = %q, want mention of missing --id", stderr)
	}
}

func TestFoldersCreate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "new", testutil.Response{Body: map[string]any{"data": "33/17"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "create", "--parent", "33", "--title", "Friends", "--module", "contacts", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"33/17"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotMethod string
	var gotFolderID string
	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "new" {
			gotMethod = req.Method
			gotFolderID = req.URL.Query().Get("folder_id")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotMethod != http.MethodPut {
		t.Errorf("create method = %q, want PUT", gotMethod)
	}
	if gotFolderID != "33" {
		t.Errorf("folder_id query param = %q, want %q", gotFolderID, "33")
	}
	wantBody := map[string]any{"folder": map[string]any{"title": "Friends", "module": "contacts"}}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("create request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestFoldersCreate_TopLevelUsesPrimaryMailRoot(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "new", testutil.Response{Body: map[string]any{"data": "default0/Projects"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "create", "--parent", privateFolderRoot, "--title", "Projects", "--module", "mail", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":"default0/Projects"}`+"\n" {
		t.Fatalf("stdout = %q, want top-level mail folder id", stdout)
	}

	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "new" {
			if got := req.URL.Query().Get("folder_id"); got != primaryMailRoot {
				t.Errorf("folder_id query param = %q, want primary mail root %q", got, primaryMailRoot)
			}
			if req.URL.Query().Has("tree") {
				t.Errorf("tree query param = %q, want primary tree default", req.URL.Query().Get("tree"))
			}
			return
		}
	}
	t.Fatal("no folders create request observed")
}

func TestFoldersGet_MailFolderAllowsNullTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "default0/Projects", "title": "Projects", "module": "mail", "folder_id": "1",
			"subfolders": false, "last_modified": nil,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "get", "--id", "default0/Projects", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"default0/Projects","title":"Projects","module":"mail","parent_id":"1","has_subfolders":false,"last_modified":null,"permissions":null}` + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestFoldersGet_MailFolderRejectsNonPositiveTimestamp(t *testing.T) {
	for _, timestamp := range []int64{0, -1} {
		t.Run(strconv.FormatInt(timestamp, 10), func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/folders", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{
					"id": "default0/Projects", "title": "Projects", "module": "mail", "folder_id": "1",
					"subfolders": false, "last_modified": timestamp,
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"folders", "get", "--id", "default0/Projects", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want non-positive timestamp rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestFoldersCreate_MissingRequiredFlags_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"folders", "create", "--parent", "33", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--title") {
		t.Errorf("stderr = %q, want mention of missing --title", stderr)
	}
}

func TestFoldersCreate_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "create", "--parent", "33", "--title", "Friends", "--module", "contacts", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" {
			t.Fatalf("dry-run sent a request to /ajax/folders: %s", req.URL)
		}
	}
}

func TestFoldersUpdate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	// last_modified is a Time value shifted to the user's time zone (here +2 hours); the
	// optimistic-lock timestamp must come from the response envelope instead.
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700007200000,
		}, "timestamp": 1700000000000},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "33/17"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "33/17", "--title", "Close Friends", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"33/17"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var foundGet bool
	var gotMethod string
	var gotID string
	var gotTimestamp string
	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "get" {
			foundGet = true
			if req.Method != http.MethodGet || req.URL.Query().Get("id") != "33/17" {
				t.Errorf("preflight request = %s %s, want GET id=33/17", req.Method, req.URL)
			}
		}
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "update" {
			gotMethod = req.Method
			gotID = req.URL.Query().Get("id")
			gotTimestamp = req.URL.Query().Get("timestamp")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if !foundGet {
		t.Error("no get preflight request observed")
	}
	if gotMethod != http.MethodPut {
		t.Errorf("update method = %q, want PUT", gotMethod)
	}
	if gotID != "33/17" {
		t.Errorf("id query param = %q, want 33/17", gotID)
	}
	if gotTimestamp != "1700000000000" {
		t.Errorf("timestamp query param = %q, want %q (from prior get)", gotTimestamp, "1700000000000")
	}
	wantBody := map[string]any{"folder": map[string]any{"title": "Close Friends"}}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("update request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestFoldersUpdate_RejectsMissingLookupTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false,
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "33/17"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "33/17", "--title", "Close Friends", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want missing optimistic-lock timestamp rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "update" {
			t.Fatalf("sent update with missing lookup timestamp: %s", req.URL)
		}
	}
}

func TestFoldersUpdate_BlankResponseIDFallsBackToRequestedID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": ""}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "33/17", "--title", "Close Friends", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":"33/17"}`+"\n" {
		t.Fatalf("stdout = %q, want requested folder id fallback", stdout)
	}
}

func TestFoldersUpdate_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "33/18"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "33/17", "--title", "Close Friends", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched folder id rejected; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFoldersUpdate_MissingTitle_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"folders", "update", "--id", "33/17", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--title") || !strings.Contains(stderr, "--permissions") {
		t.Errorf("stderr = %q, want mention of missing --title and --permissions", stderr)
	}
}

func TestFoldersUpdate_Permissions_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700000000000,
			"permissions": []any{map[string]any{"entity": 3, "bits": 403710016, "group": false}},
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "33/17"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "33/17",
		"--permissions", `[{"entity":3,"bits":403710016,"group":false},{"entity":4,"bits":257,"group":false}]`,
		"--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"33/17"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "update" {
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	folderPayload := requireFolderRequestPayload(t, gotBody)
	if _, hasTitle := folderPayload["title"]; hasTitle {
		t.Errorf("request folder payload = %v, want no title key when only --permissions given", folderPayload)
	}
	perms, ok := folderPayload["permissions"].([]any)
	if !ok || len(perms) != 2 {
		t.Fatalf("request body permissions = %v, want 2 permission entries", folderPayload["permissions"])
	}
	first := perms[0].(map[string]any)
	if first["entity"] != float64(3) || first["bits"] != float64(403710016) || first["group"] != false {
		t.Errorf("first permission = %v, want entity=3 bits=403710016 group=false", first)
	}
}

func TestFoldersUpdate_InvalidPermissionsJSON_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"folders", "update", "--id", "33/17", "--permissions", "not-json", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--permissions") {
		t.Errorf("stderr = %q, want mention of --permissions", stderr)
	}
}

func TestFoldersUpdate_RejectsNullPermissions(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"folders", "update", "--id", "33/17", "--permissions", "null", "--dry-run", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "array") {
		t.Fatalf("stderr = %q, want permissions-array error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid permissions made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestFoldersUpdate_RejectsUnknownPermissionFields(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"folders", "update", "--id", "33/17",
		"--permissions", `[{"entity":3,"bitts":403710016,"group":false}]`,
		"--dry-run", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want misspelled permission field rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "bitts") {
		t.Fatalf("stderr = %q, want unknown field named", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid permissions made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestFoldersUpdate_TitleAndPermissions_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "33/17"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "33/17", "--title", "Close Friends",
		"--permissions", `[{"entity":3,"bits":403710016,"group":false}]`,
		"--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"33/17"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "update" {
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	folderPayload := requireFolderRequestPayload(t, gotBody)
	if folderPayload["title"] != "Close Friends" {
		t.Errorf("request body title = %v, want %q", folderPayload["title"], "Close Friends")
	}
	if _, hasPerms := folderPayload["permissions"]; !hasPerms {
		t.Errorf("request folder payload = %v, want permissions key present", folderPayload)
	}
}

func TestFoldersUpdate_PermissionsFromFile(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700000000000,
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "33/17"}})

	dir := t.TempDir()
	permFile := dir + "/perms.json"
	if err := os.WriteFile(permFile, []byte(`[{"entity":5,"bits":1,"group":true}]`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "33/17", "--permissions", "@" + permFile, "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":"33/17"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}

	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "update" {
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	folderPayload := requireFolderRequestPayload(t, gotBody)
	perms, ok := folderPayload["permissions"].([]any)
	if !ok || len(perms) != 1 {
		t.Fatalf("request body permissions = %v, want 1 entry from file", folderPayload["permissions"])
	}
}

func TestFoldersGet_IncludesPermissions_LLMMode(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "32", "title": "Calendar", "module": "calendar", "folder_id": "1",
			"subfolders": false, "last_modified": 1700000000000,
			"permissions": []any{map[string]any{"entity": 3, "bits": 403710016, "group": false}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "get", "--id", "32", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"permissions":[{"entity":3,"bits":403710016,"group":false}]`) {
		t.Errorf("stdout = %q, want permissions field present", stdout)
	}
}

func TestFoldersGet_PermissionsRemainSelectable(t *testing.T) {
	tests := []struct {
		name        string
		permissions []any
		want        string
	}{
		{name: "empty", permissions: []any{}, want: `{"permissions":[]}` + "\n"},
		{
			name: "populated",
			permissions: []any{
				map[string]any{"entity": 3, "bits": 403710016, "group": false},
			},
			want: `{"permissions":[{"bits":403710016,"entity":3,"group":false}]}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/folders", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{
					"id": "32", "title": "Calendar", "module": "calendar", "folder_id": "1",
					"subfolders": false, "last_modified": 1700000000000,
					"permissions": tt.permissions,
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"folders", "get", "--id", "32", "--format", "json", "--select", "permissions",
			})

			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if stdout != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
}

func TestFoldersDelete_MissingConfirm_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "delete", "--id", "33/17", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want mention of --confirm", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls, got %d requests", len(srv.Requests()))
	}
}

func TestFoldersDelete_ConfirmMismatch_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "delete", "--id", "33/17", "--confirm", "wrong-id", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want mention of confirmation mismatch", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls, got %d requests", len(srv.Requests()))
	}
}

func TestFoldersDelete_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "delete", "--id", "33/17", "--confirm", "33/17", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" {
			t.Fatalf("dry-run sent a request to /ajax/folders: %s", req.URL)
		}
	}
}

func TestFoldersDelete_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700007200000,
		}, "timestamp": 1700000000000},
	})
	srv.Handle("/ajax/folders", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "delete", "--id", "33/17", "--confirm", "33/17", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"deleted":true,"id":"33/17"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var foundGet bool
	var gotMethod string
	var gotTimestamp string
	var gotBody []string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "get" {
			foundGet = true
			if req.Method != http.MethodGet || req.URL.Query().Get("id") != "33/17" {
				t.Errorf("preflight request = %s %s, want GET id=33/17", req.Method, req.URL)
			}
		}
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "delete" {
			gotMethod = req.Method
			gotTimestamp = req.URL.Query().Get("timestamp")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if !foundGet {
		t.Error("no get preflight request observed")
	}
	if gotMethod != http.MethodPut {
		t.Errorf("delete method = %q, want PUT", gotMethod)
	}
	if gotTimestamp != "1700000000000" {
		t.Errorf("timestamp query param = %q, want 1700000000000", gotTimestamp)
	}
	if len(gotBody) != 1 || gotBody[0] != "33/17" {
		t.Errorf("request body = %v, want [\"33/17\"]", gotBody)
	}
}

func TestFoldersDelete_MailFolderOmitsNullTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "default0/Projects", "title": "Projects", "module": "mail", "folder_id": "1",
			"subfolders": false, "last_modified": nil,
		}},
	})
	srv.Handle("/ajax/folders", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "delete", "--id", "default0/Projects", "--confirm", "default0/Projects", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"deleted":true,"id":"default0/Projects"}`+"\n" {
		t.Fatalf("stdout = %q, want successful delete", stdout)
	}

	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "delete" {
			if req.URL.Query().Has("timestamp") {
				t.Errorf("timestamp query param = %q, want omitted for mail folder", req.URL.Query().Get("timestamp"))
			}
			return
		}
	}
	t.Fatal("no folders delete request observed")
}

// TestFoldersUpdate_MailFolderOmitsNullTimestamp is the regression test for renaming a mail
// folder: mail folders have no last_modified, and update refused every one of them even
// though the timestamp parameter is optional.
func TestFoldersUpdate_MailFolderOmitsNullTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "default0/Projects", "title": "Projects", "module": "mail", "folder_id": "1",
			"subfolders": false, "last_modified": nil,
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "default0/Projects"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"folders", "update", "--id", "default0/Projects", "--title", "Archive", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":"default0/Projects"}`+"\n" {
		t.Fatalf("stdout = %q, want the updated mail folder id", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "update" {
			if req.URL.Query().Has("timestamp") {
				t.Errorf("timestamp query param = %q, want omitted for mail folder", req.URL.Query().Get("timestamp"))
			}
			return
		}
	}
	t.Fatal("no folders update request observed")
}

func TestFoldersUpdate_FallsBackToLastModifiedUTC(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
			"subfolders": false, "last_modified": 1700007200000, "last_modified_utc": 1700000000000,
		}},
	})
	srv.Handle("/ajax/folders", "update", testutil.Response{Body: map[string]any{"data": "33/17"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"folders", "update", "--id", "33/17", "--title", "Close Friends", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/folders" && req.URL.Query().Get("action") == "update" {
			if got := req.URL.Query().Get("timestamp"); got != "1700000000000" {
				t.Errorf("timestamp query param = %q, want last_modified_utc 1700000000000", got)
			}
			return
		}
	}
	t.Fatal("no folders update request observed")
}

func TestFoldersList_MalformedResponse_InvalidResponseError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": "not-an-array"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"folders", "list", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response error", stderr)
	}
}

// The registry lists the scalar result fields and the dry-run fields of the folders
// commands, so a --select that cannot match is rejected before any request.
func TestFolders_SelectIsCheckedBeforeDispatch(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"dotted path into a scalar", []string{"folders", "get", "--id", "3", "--select", "title.x"}, `goes into \"title\"`},
		{"update dry run", []string{"folders", "update", "--id", "3", "--title", "N", "--dry-run", "--select", "deleted"}, "dry-run fields: dry_run, id, permissions, title"},
		{"delete dry run", []string{"folders", "delete", "--id", "3", "--confirm", "3", "--dry-run", "--select", "deleted"}, "dry-run fields: dry_run, id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, env, tc.args)
			if code != 4 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want invalid_argument containing %q", code, stdout, stderr, tc.want)
			}
			if n := len(srv.Requests()); n != 0 {
				t.Errorf("sent %d requests, want none", n)
			}
		})
	}
}
