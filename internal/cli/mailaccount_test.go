// ABOUTME: CLI integration tests for the account resource: list/get/create/update/delete,
// ABOUTME: human and LLM output, validation, dry-run, confirm safeguards, and API errors.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestAccountList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	// The live deployment returns column-tuples (an array of arrays), positioned per the
	// requested "columns" param (id, name, primary_address, login), not an array of objects.
	srv.Handle("/ajax/account", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{0, "E-Mail", "fixture-user@example.invalid", nil},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "fixture-user@example.invalid") {
		t.Errorf("stdout = %q, want primary_address present", stdout)
	}

	for _, r := range srv.Requests() {
		if r.URL.Query().Get("action") == "all" {
			if got := r.URL.Query().Get("columns"); got == "" {
				t.Errorf("columns param missing, required by the OX account?action=all contract")
			}
		}
	}
}

func TestAccountList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{0, "E-Mail", "fixture-user@example.invalid", nil},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	want := `{"accounts":[{"id":0,"name":"E-Mail","primary_address":"fixture-user@example.invalid","login":""}]}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

}

func TestAccountList_EmptyLoginRemainsSelectable(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{Body: map[string]any{"data": []any{
		[]any{0, "Primary", "primary@example.org", ""},
	}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"account", "list", "--format", "json", "--select", "login",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"accounts":[{"login":""}]}` + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestAccountList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountList_RejectsUnexpectedExtraColumns(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{0, "E-Mail", "fixture-user@example.invalid", nil, "unexpected"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure for a row wider than the requested schema; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountList_RejectsNegativeAccountID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{-1, "Invalid", "invalid@example.com", "invalid"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure for a negative account id; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountList_RejectsNullName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{0, nil, "fixture-user@example.invalid", nil},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want null account name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountList_RejectsNullPrimaryAddress(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{0, "E-Mail", nil, nil},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want null primary address rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountGet_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "name": "E-Mail", "primary_address": "fixture-user@example.invalid",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "get", "--id", "0"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "fixture-user@example.invalid") {
		t.Errorf("stdout = %q, want primary_address present", stdout)
	}
}

func TestAccountGet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "name": "E-Mail", "primary_address": "fixture-user@example.invalid", "login": "fixture-user@example.invalid",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "get", "--id", "0", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":0,"name":"E-Mail","primary_address":"fixture-user@example.invalid","login":"fixture-user@example.invalid"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestAccountGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 6, "name": "Wrong Account", "primary_address": "wrong@example.com",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"account", "get", "--id", "5", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountGet_RejectsMissingZeroID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"name": "E-Mail", "primary_address": "fixture-user@example.invalid",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "get", "--id", "0", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing id rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountGet_RejectsMissingName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "primary_address": "fixture-user@example.invalid",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "get", "--id", "0", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing account name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountGet_RejectsMissingPrimaryAddress(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "name": "E-Mail",
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "get", "--id", "0", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing primary address rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAccountGet_MissingID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"account", "get", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr, "--id is required") {
		t.Errorf("stderr = %q, want --id required message", stderr)
	}
}

func TestAccountActions_RejectNegativeIDsLocally(t *testing.T) {
	tests := map[string][]string{
		"get":    {"account", "get", "--id", "-1", "--llm"},
		"update": {"account", "update", "--id", "-1", "--name", "Invalid", "--dry-run", "--llm"},
		"delete": {"account", "delete", "--id", "-1", "--confirm", "-1", "--dry-run", "--llm"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code == 0 {
				t.Fatalf("exit code = 0, want negative id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "--id") {
				t.Fatalf("stderr = %q, want id validation error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("negative id made %d requests, want local rejection", len(srv.Requests()))
			}
		})
	}
}

func TestAccountGet_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "get", testutil.Response{
		Body: map[string]any{"error": "not found", "code": "ACC-0001", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, _, code := run(t, srv, env, []string{"account", "get", "--id", "99", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on error", stdout)
	}
}

func TestAccountCreate_DryRun_DoesNotCallAPI(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"account", "create", "--login", "foo", "--password", "bar",
		"--mail-url", "imap://host:993", "--primary-address", "foo@example.com", "--llm", "--dry-run",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests to be sent during dry-run, got %d", len(srv.Requests()))
	}
}

func TestAccountCreate_MissingRequiredArgs(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"missing login", []string{"account", "create", "--password", "p", "--mail-url", "u", "--primary-address", "a"}, "--login is required"},
		{"missing password", []string{"account", "create", "--login", "l", "--mail-url", "u", "--primary-address", "a"}, "a password is required"},
		{"missing mail-url", []string{"account", "create", "--login", "l", "--password", "p", "--primary-address", "a"}, "--mail-url is required"},
		{"missing primary-address", []string{"account", "create", "--login", "l", "--password", "p", "--mail-url", "u"}, "--primary-address is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, code := run(t, srv, env, tc.args)
			if code == 0 {
				t.Fatalf("exit code = %d, want non-zero", code)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr = %q, want %q", stderr, tc.want)
			}
		})
	}
}

func TestAccountCreate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 5}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"account", "create", "--login", "foo", "--password", "bar",
		"--mail-url", "imap://host:993", "--primary-address", "foo@example.com",
		"--name", "External Foo", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":5}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var createRequest *http.Request
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/account" && req.URL.Query().Get("action") == "new" {
			createRequest = req
			break
		}
	}
	if createRequest == nil {
		t.Fatal("expected an account create request")
	}
	if createRequest.Method != http.MethodPut {
		t.Errorf("create request method = %q, want PUT", createRequest.Method)
	}
	var createBody map[string]string
	if err := json.NewDecoder(createRequest.Body).Decode(&createBody); err != nil {
		t.Fatalf("decode account create request body: %v", err)
	}
	if len(createBody) != 5 {
		t.Errorf("account create request has %d fields, want 5", len(createBody))
	}
	for field, wantValue := range map[string]string{
		"login":           "foo",
		"mail_url":        "imap://host:993",
		"primary_address": "foo@example.com",
		"name":            "External Foo",
	} {
		if createBody[field] != wantValue {
			t.Errorf("account create %s = %q, want %q", field, createBody[field], wantValue)
		}
	}
	if createBody["password"] != "bar" {
		t.Error("account create password field did not match the supplied password")
	}
}

func TestAccountCreate_RejectsNonPositiveID(t *testing.T) {
	for _, id := range []int{0, -1} {
		t.Run(strconv.Itoa(id), func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/account", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": id}}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"account", "create", "--login", "foo", "--password", "bar",
				"--mail-url", "imap://host:993", "--primary-address", "foo@example.com", "--llm",
			})

			if code == 0 {
				t.Fatalf("exit code = 0, want non-positive created id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestAccountUpdate_RequiresAtLeastOneField(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"account", "update", "--id", "5"})

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr, "at least one of") {
		t.Errorf("stderr = %q, want 'at least one of' message", stderr)
	}
}

func TestAccountUpdate_DryRun_DoesNotCallAPI(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "update", "--id", "5", "--name", "New Name", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests to be sent during dry-run, got %d", len(srv.Requests()))
	}
}

func TestAccountUpdate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "update", "--id", "5", "--name", "New Name", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":5}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var updateRequest *http.Request
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/account" && req.URL.Query().Get("action") == "update" {
			updateRequest = req
			break
		}
	}
	if updateRequest == nil {
		t.Fatal("expected an account update request")
	}
	if updateRequest.Method != http.MethodPut {
		t.Errorf("update request method = %q, want PUT", updateRequest.Method)
	}
	if updateRequest.URL.Query().Get("id") != "5" {
		t.Errorf("update request id query = %q, want 5", updateRequest.URL.Query().Get("id"))
	}
	var updateBody map[string]json.RawMessage
	if err := json.NewDecoder(updateRequest.Body).Decode(&updateBody); err != nil {
		t.Fatalf("decode account update request body: %v", err)
	}
	if len(updateBody) != 2 {
		t.Errorf("account update request has %d fields, want 2", len(updateBody))
	}
	var updateID int
	if err := json.Unmarshal(updateBody["id"], &updateID); err != nil || updateID != 5 {
		t.Errorf("account update body id was not numeric 5")
	}
	var updateName string
	if err := json.Unmarshal(updateBody["name"], &updateName); err != nil || updateName != "New Name" {
		t.Errorf("account update body name = %q, want %q", updateName, "New Name")
	}
}

func TestAccountDelete_RequiresConfirm(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"account", "delete", "--id", "5"})

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr, "--confirm is required") {
		t.Errorf("stderr = %q, want --confirm required message", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests to be sent without confirm, got %d", len(srv.Requests()))
	}
}

func TestAccountDelete_ConfirmMismatch(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"account", "delete", "--id", "5", "--confirm", "6"})

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr, "does not match") {
		t.Errorf("stderr = %q, want mismatch message", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests to be sent on confirm mismatch, got %d", len(srv.Requests()))
	}
}

func TestAccountDelete_DryRun_DoesNotCallAPI(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "delete", "--id", "5", "--confirm", "5", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests to be sent during dry-run, got %d", len(srv.Requests()))
	}
}

func TestAccountDelete_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "delete", testutil.Response{Body: map[string]any{"data": []any{5}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"account", "delete", "--id", "5", "--confirm", "5", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"deleted":true,"id":5}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var deleteBody string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/account" && req.URL.Query().Get("action") == "delete" {
			if req.Method != http.MethodPut {
				t.Errorf("delete request method = %q, want PUT", req.Method)
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read delete request body: %v", err)
			}
			deleteBody = string(body)
		}
	}
	if deleteBody != `[5]` {
		t.Errorf("delete request body = %q, want JSON account-id array [5]", deleteBody)
	}
}

func TestAccountDelete_RejectsUnconfirmedResult(t *testing.T) {
	for name, result := range map[string]any{
		"empty":        []any{},
		"wrong id":     []any{6},
		"multiple ids": []any{5, 6},
		"object":       map[string]any{},
		"null":         nil,
	} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/account", "delete", testutil.Response{Body: map[string]any{"data": result}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{"account", "delete", "--id", "5", "--confirm", "5", "--llm"})
			if code == 0 {
				t.Fatalf("exit code = 0, want unconfirmed account deletion rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestAccount_UnknownAction(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"account", "bogus"})

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr, "unknown account action") {
		t.Errorf("stderr = %q, want unknown action message", stderr)
	}
}

func TestAccount_NoAction(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"account"})

	if code == 0 {
		t.Fatalf("exit code = %d, want non-zero", code)
	}
	if !strings.Contains(stderr, "account requires an action") {
		t.Errorf("stderr = %q, want requires-action message", stderr)
	}
}
