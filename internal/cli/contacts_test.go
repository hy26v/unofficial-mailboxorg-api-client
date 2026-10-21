// ABOUTME: CLI integration tests for the contacts list command: human and LLM output,
// ABOUTME: bounded limit validation, authentication failures, API failures, and discovery.
package cli

import (
	"net/url"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func contactsTestServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "6", "user_id": 3}},
	})
	srv.Handle("/ajax/contacts", "all", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"2", "Robin Example", "Robin", "Example", "fixture-user@example.invalid"},
		}},
	})
	return srv
}

func TestContactsList_HumanMode_Success(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Robin Example") || !strings.Contains(stdout, "fixture-user@example.invalid") {
		t.Errorf("stdout = %q, want contact fields present", stdout)
	}
}

func TestContactsList_HumanMode_EscapesControlCharacters(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"folder_id": "6"}},
	})
	srv.Handle("/ajax/contacts", "all", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"2", "Robin\tAdmin\nInjected", "Robin", "Example", "fixture-user@example.invalid"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `Robin\tAdmin\nInjected`) {
		t.Errorf("stdout = %q, want control characters escaped inside one cell", stdout)
	}
	if got := strings.Count(stdout, "\n"); got != 2 {
		t.Errorf("stdout has %d lines, want one header and one contact row: %q", got, stdout)
	}
}

func TestContactsList_LLMMode_CompactJSON(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	want := `{"contacts":[{"id":"2","display_name":"Robin Example","first_name":"Robin","last_name":"Example","email":"fixture-user@example.invalid"}],"truncated":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestContactsList_RequestsUseFolderFromUserGetAndBoundedDefaultLimit(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"contacts", "list"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var contactsReq *url.URL
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/contacts" {
			contactsReq = r.URL
		}
	}
	if contactsReq == nil {
		t.Fatal("no request to /ajax/contacts observed")
	}
	q := contactsReq.Query()
	if q.Get("folder") != "6" {
		t.Errorf("folder = %q, want 6 (from user.get folder_id)", q.Get("folder"))
	}
	if q.Get("right_hand_limit") == "" {
		t.Errorf("right_hand_limit not set, want a bounded default limit")
	}
}

func TestContactsList_LimitFlag_SetsRightHandLimit(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"contacts", "list", "--limit", "5"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var contactsReq *url.URL
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/contacts" {
			contactsReq = r.URL
		}
	}
	if contactsReq == nil {
		t.Fatal("no request to /ajax/contacts observed")
	}
	// One past --limit: the range is applied server-side, so the extra row is what lets the
	// command tell a folder holding exactly --limit contacts from one holding more.
	if contactsReq.Query().Get("right_hand_limit") != "6" {
		t.Errorf("right_hand_limit = %q, want 6 for --limit 5", contactsReq.Query().Get("right_hand_limit"))
	}
}

func TestContactsList_InvalidLimit_InvalidArgumentError(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "list", "--limit", "bogus", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--limit") {
		t.Errorf("stderr = %q, want mention of --limit", stderr)
	}
}

func TestContactsList_LimitExceedsMax_InvalidArgumentError(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "list", "--limit", "10000", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--limit") {
		t.Errorf("stderr = %q, want mention of --limit", stderr)
	}
}

func TestContactsList_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "list", "--bogus"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should describe the invalid flag")
	}
}

func TestContactsList_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "bogus"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should describe the invalid action")
	}
}

func TestContactsList_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := contactsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"contacts", "list", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(stderr), `{"error":`) {
		t.Errorf("stderr = %q, want structured error JSON", stderr)
	}
}

func TestContactsList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "list", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "SES-0203") {
		t.Errorf("stderr = %q, want SES-0203 code", stderr)
	}
}

func TestContactsList_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"contacts", "list"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_ContactsList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "contacts.list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"contacts.list"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestCommands_LLM_ListsContactsList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "contacts", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"contacts.list"`) {
		t.Errorf("stdout = %q, want contacts.list listed", stdout)
	}
}

// The registry lists the scalar result fields and the dry-run fields of the contacts
// commands, so a --select that cannot match is rejected before any request.
func TestContacts_SelectIsCheckedBeforeDispatch(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"dotted path into a scalar", []string{"contacts", "get", "--id", "5", "--select", "id.x"}, `goes into \"id\"`},
		{"write result field in a dry run", []string{"contacts", "create", "--field", "a=b", "--dry-run", "--select", "id"}, "dry-run fields: action, dry_run, fields, folder_id"},
		{"import dry run", []string{"contacts", "import", "--file", "contacts_test.go", "--dry-run", "--select", "count"}, "dry-run fields: dry_run, filename, folder_id"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := contactsTestServer()
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

	srv := contactsTestServer()
	defer srv.Close()
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "update", "--id", "5", "--timestamp", "9", "--field", "a=b", "--dry-run", "--select", "fields.a"})
	if code != 0 || stdout != `{"fields.a":"b"}`+"\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want the selected dry-run field", code, stdout, stderr)
	}
}
