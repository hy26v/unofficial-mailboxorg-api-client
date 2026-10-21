// ABOUTME: CLI integration tests for contacts get/create/update/delete: request mapping,
// ABOUTME: structured input, optimistic locking, and the delete confirmation safeguard.
package cli

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func contactsCRUDTestServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "33", "user_id": 3}},
	})
	return srv
}

// --- contacts get ---

func TestContactsGet_HumanMode_Success(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "5", "display_name": "Test Contact", "first_name": "Test", "last_name": "Contact",
			"email1": "test-contact@example.com", "folder_id": "33", "last_modified": 1784862966136,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "get", "--id", "5"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Test Contact") || !strings.Contains(stdout, "test-contact@example.com") {
		t.Errorf("stdout = %q, want contact fields present", stdout)
	}
}

func TestContactsGet_LLMMode_UsesDefaultFolderWhenOmitted(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "5", "display_name": "Test Contact", "folder_id": "33", "last_modified": 1784862966136,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "get", "--id", "5", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var contactsReq *testResolvedRequest
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/contacts" {
			contactsReq = &testResolvedRequest{query: r.URL.Query()}
		}
	}
	if contactsReq == nil {
		t.Fatal("no request to /ajax/contacts observed")
	}
	if contactsReq.query.Get("folder") != "33" {
		t.Errorf("folder = %q, want 33 (from user.get folder_id)", contactsReq.query.Get("folder"))
	}
	if !strings.Contains(stdout, `"id":"5"`) {
		t.Errorf("stdout = %q, want id field", stdout)
	}
}

func TestContactsGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "6", "display_name": "Wrong Contact", "folder_id": "33", "last_modified": 1784862966136,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "get", "--id", "5", "--folder", "33", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestContactsGet_RejectsMismatchedResponseFolder(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "5", "display_name": "Wrong folder", "folder_id": "34", "last_modified": 1784862966136,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "get", "--id", "5", "--folder", "33", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure for a contact from another folder; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestContactsGet_RejectsNonPositiveLastModified(t *testing.T) {
	for _, lastModified := range []int64{0, -1} {
		t.Run(strconv.FormatInt(lastModified, 10), func(t *testing.T) {
			srv := contactsCRUDTestServer()
			defer srv.Close()
			srv.Handle("/ajax/contacts", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{
					"id": "5", "display_name": "Invalid timestamp", "folder_id": "33", "last_modified": lastModified,
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"contacts", "get", "--id", "5", "--folder", "33", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want last_modified=%d rejected; stdout=%s", lastModified, stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestContactsGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "get", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--id") {
		t.Errorf("stderr = %q, want mention of --id", stderr)
	}
}

// --- contacts create ---

func TestContactsCreate_FieldFlags_Success(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "5"}, "timestamp": 1784855765608},
	})
	srv.Handle("/ajax/contacts", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "5", "display_name": "Test Contact", "first_name": "Test", "last_name": "Contact",
			"email1": "test-contact@example.com", "folder_id": "33", "last_modified": 1784855765608,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "create",
		"--field", "display_name=Test Contact",
		"--field", "first_name=Test",
		"--field", "last_name=Contact",
		"--field", "email1=test-contact@example.com",
		"--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"id":"5"`) {
		t.Errorf("stdout = %q, want created id", stdout)
	}

	var newReq *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/contacts" && r.URL.Query().Get("action") == "new" {
			newReq = r
			break
		}
	}
	if newReq == nil {
		t.Fatal("no request to /ajax/contacts?action=new observed")
	}
	if newReq.Method != http.MethodPut {
		t.Errorf("create request method = %q, want PUT", newReq.Method)
	}
	var createBody map[string]string
	if err := json.NewDecoder(newReq.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode contacts create request body: %v", err)
	}
	if len(createBody) != 5 {
		t.Errorf("contacts create body has %d fields, want 5", len(createBody))
	}
	for field, want := range map[string]string{
		"display_name": "Test Contact",
		"first_name":   "Test",
		"last_name":    "Contact",
		"email1":       "test-contact@example.com",
		"folder_id":    "33",
	} {
		if createBody[field] != want {
			t.Errorf("contacts create body %s = %q, want %q", field, createBody[field], want)
		}
	}
}

func TestContactsCreate_InputFlag_FromFile(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "6"}, "timestamp": 1784855765608},
	})
	srv.Handle("/ajax/contacts", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "6", "display_name": "From File", "folder_id": "33"}},
	})

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "contact.json")
	if err := os.WriteFile(inputPath, []byte(`{"display_name":"From File"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "create", "--input", "@" + inputPath, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"id":"6"`) {
		t.Errorf("stdout = %q, want created id", stdout)
	}
}

func TestContactsCreate_InvalidInputJSON_InvalidArgumentError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "create", "--input", "{not json", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--input") {
		t.Errorf("stderr = %q, want mention of --input", stderr)
	}
}

func TestContactsCreate_FieldFlagWithoutEquals_InvalidArgumentError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "create", "--field", "bogus", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--field") {
		t.Errorf("stderr = %q, want mention of --field", stderr)
	}
}

func TestContactsCreate_NoFieldsOrInput_InvalidArgumentError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "create", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--field") && !strings.Contains(stderr, "--input") {
		t.Errorf("stderr = %q, want mention of --field or --input", stderr)
	}
}

func TestContactsCreate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "create", "--field", "display_name=Test", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
}

func TestContactsDryRuns_DoNotRequireCredentials(t *testing.T) {
	tests := map[string][]string{
		"create": {"contacts", "create", "--field", "display_name=Preview", "--dry-run", "--llm"},
		"update": {"contacts", "update", "--id", "5", "--timestamp", "123", "--field", "display_name=Preview", "--dry-run", "--llm"},
		"delete": {"contacts", "delete", "--id", "5", "--timestamp", "123", "--confirm", "5", "--dry-run", "--llm"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if !strings.Contains(stdout, `"dry_run":true`) {
				t.Fatalf("stdout = %q, want dry-run preview", stdout)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("dry run made HTTP requests: %+v", srv.Requests())
			}
		})
	}
}

// --- contacts update ---

func TestContactsUpdate_Success(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{}, "timestamp": 1784862966999},
	})
	srv.Handle("/ajax/contacts", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "5", "display_name": "Updated", "folder_id": "33", "last_modified": 1784862966999,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "update", "--id", "5", "--timestamp", "1784862966136",
		"--field", "display_name=Updated", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"display_name":"Updated"`) {
		t.Errorf("stdout = %q, want updated display_name", stdout)
	}

	var updateReq *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/contacts" && r.URL.Query().Get("action") == "update" {
			updateReq = r
			break
		}
	}
	if updateReq == nil {
		t.Fatal("no request to /ajax/contacts?action=update observed")
	}
	if updateReq.Method != http.MethodPut {
		t.Errorf("update request method = %q, want PUT", updateReq.Method)
	}
	if updateReq.URL.Query().Get("timestamp") != "1784862966136" {
		t.Errorf("timestamp = %q, want 1784862966136", updateReq.URL.Query().Get("timestamp"))
	}
	if updateReq.URL.Query().Get("id") != "5" {
		t.Errorf("id = %q, want 5", updateReq.URL.Query().Get("id"))
	}
	if updateReq.URL.Query().Get("folder") != "33" {
		t.Errorf("folder = %q, want 33", updateReq.URL.Query().Get("folder"))
	}
	var updateBody map[string]string
	if err := json.NewDecoder(updateReq.Body).Decode(&updateBody); err != nil {
		t.Fatalf("decode contacts update request body: %v", err)
	}
	if len(updateBody) != 1 || updateBody["display_name"] != "Updated" {
		t.Errorf("contacts update body display_name/field count = %q/%d, want %q/1",
			updateBody["display_name"], len(updateBody), "Updated")
	}
}

func TestContactsUpdate_MissingTimestamp_InvalidArgumentError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"contacts", "update", "--id", "5", "--field", "display_name=Updated", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--timestamp") {
		t.Errorf("stderr = %q, want mention of --timestamp", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want 0 (should refuse locally)", len(srv.Requests()))
	}
}

func TestContactsUpdate_MissingID_InvalidArgumentError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"contacts", "update", "--timestamp", "1", "--field", "display_name=Updated", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--id") {
		t.Errorf("stderr = %q, want mention of --id", stderr)
	}
}

func TestContactsMutations_RejectNonPositiveTimestamp(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "update",
			args: []string{"contacts", "update", "--id", "5", "--timestamp", "0", "--field", "display_name=Updated", "--dry-run", "--llm"},
		},
		{
			name: "delete",
			args: []string{"contacts", "delete", "--id", "5", "--timestamp", "-1", "--confirm", "5", "--dry-run", "--llm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "positive") {
				t.Fatalf("stderr = %q, want positive timestamp error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("invalid timestamp made %d HTTP requests, want 0", len(srv.Requests()))
			}
		})
	}
}

func TestContactsUpdate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "update", "--id", "5", "--timestamp", "1", "--field", "display_name=X", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
}

// --- contacts delete (destructive) ---

func TestContactsDelete_MissingConfirm_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"contacts", "delete", "--id", "5", "--folder", "33", "--timestamp", "1", "--llm",
	})

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
		t.Errorf("made %d requests, want 0 (should refuse locally without --confirm)", len(srv.Requests()))
	}
}

func TestContactsDelete_ConfirmMismatch_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"contacts", "delete", "--id", "5", "--folder", "33", "--timestamp", "1", "--confirm", "99", "--llm",
	})

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
		t.Errorf("made %d requests, want 0 (should refuse locally on confirm mismatch)", len(srv.Requests()))
	}
}

func TestContactsDelete_MissingTimestamp_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"contacts", "delete", "--id", "5", "--folder", "33", "--confirm", "5", "--llm",
	})
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if !strings.Contains(stderr, "--timestamp") {
		t.Errorf("stderr = %q, want mention of --timestamp", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want 0", len(srv.Requests()))
	}
}

func TestContactsDelete_Success(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/contacts", "delete", testutil.Response{
		Body: map[string]any{"data": map[string]any{}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "delete", "--id", "5", "--folder", "33", "--timestamp", "1", "--confirm", "5", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"id":"5"`) || !strings.Contains(stdout, `"deleted":true`) {
		t.Errorf("stdout = %q, want deleted confirmation", stdout)
	}

	var deleteReq *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/contacts" && r.URL.Query().Get("action") == "delete" {
			deleteReq = r
			break
		}
	}
	if deleteReq == nil {
		t.Fatal("no request to /ajax/contacts?action=delete observed")
	}
	if deleteReq.Method != http.MethodPut {
		t.Errorf("delete request method = %q, want PUT", deleteReq.Method)
	}
	if deleteReq.URL.Query().Get("timestamp") != "1" {
		t.Errorf("timestamp = %q, want 1", deleteReq.URL.Query().Get("timestamp"))
	}
	var deleteBody []map[string]string
	if err := json.NewDecoder(deleteReq.Body).Decode(&deleteBody); err != nil {
		t.Fatalf("decode contacts delete request body: %v", err)
	}
	if len(deleteBody) != 1 || len(deleteBody[0]) != 2 ||
		deleteBody[0]["id"] != "5" || deleteBody[0]["folder"] != "33" {
		t.Errorf("contacts delete body id/folder/item count = %q/%q/%d, want %q/%q/1",
			deleteBody[0]["id"], deleteBody[0]["folder"], len(deleteBody), "5", "33")
	}
}

func TestContactsDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "delete", "--id", "5", "--folder", "33", "--timestamp", "1", "--confirm", "5", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
}

// testResolvedRequest carries the query values of an observed test-server request.
type testResolvedRequest struct {
	query url.Values
}
