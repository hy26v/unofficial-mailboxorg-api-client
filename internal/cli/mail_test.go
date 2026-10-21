// ABOUTME: CLI integration tests for the mail list command: human and LLM output,
// ABOUTME: validation, authentication failures, API failures, and stdout/stderr/exit-code separation.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestMailList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"7", "mailbox invoice", []any{[]any{"mailbox", "noreply@mailbox.org"}}, 1784851574000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--folder", "default0/INBOX"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "mailbox invoice") || !strings.Contains(stdout, "noreply@mailbox.org") {
		t.Errorf("stdout = %q, want subject and from present", stdout)
	}
}

func TestMailList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"7", "mailbox invoice", []any{[]any{"mailbox", "noreply@mailbox.org"}}, 1784851574000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--folder", "default0/INBOX", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("stdout not JSON: %q", stdout)
	}
	want := `{"items":[{"id":"7","subject":"mailbox invoice","from":"noreply@mailbox.org","date":"2026-07-24T00:06:14Z"}],"truncated":false,"count":1,"offset":0,"limit":20,"has_more":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestMailList_RejectsRowWithoutID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"", "unusable message", []any{}, 1784851574000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailList_TSVSelectionUnwrapsItemsBesideMetadata(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"7", "mailbox invoice", []any{[]any{"mailbox", "noreply@mailbox.org"}}, 1784851574000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "list", "--folder", "default0/INBOX",
		"--format", "tsv", "--select", "id,subject",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if want := "id\tsubject\n7\tmailbox invoice\n"; stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestMailList_DefaultFolderIsInbox(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	reqs := srv.Requests()
	var found bool
	for _, r := range reqs {
		if r.URL.Path == "/ajax/mail" && r.URL.Query().Get("folder") == "default0/INBOX" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected request with folder=default0/INBOX; requests=%v stdout=%q stderr=%q", reqs, stdout, stderr)
	}
}

// TestMailList_LimitIsSentAndBounded checks the provider's documented range parameters.
// The right-hand limit is one past the caller's page so the extra row can set has_more.
func TestMailList_LimitIsSentAndBounded(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--limit", "5", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	_ = stdout

	reqs := srv.Requests()
	var found bool
	for _, r := range reqs {
		if r.URL.Path == "/ajax/mail" && r.URL.Query().Get("left_hand_limit") == "0" && r.URL.Query().Get("right_hand_limit") == "6" && !r.URL.Query().Has("limit") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected request range [0,6) for --limit 5; requests=%v stderr=%q", reqs, stderr)
	}
}

func TestMailList_InvalidLimit_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "list", "--limit", "0", "--llm"})

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

func TestMailList_BlankToDomainIsRejectedOnlyForTheRealFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	_, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "list", "--to-domain", " ", "--llm"})
	if code != 4 || !strings.Contains(stderr, "--to-domain must not be blank") {
		t.Fatalf("blank --to-domain: code=%d stderr=%q; want the blank error", code, stderr)
	}

	// "--to-domain" here is the value of --folder, not the flag, so it must not trigger
	// the blank check. With no credentials the command then stops at sign-in.
	_, stderr, code = run(t, srv, map[string]string{}, []string{"mail", "list", "--folder", "--to-domain", "--llm"})
	if strings.Contains(stderr, "--to-domain must not be blank") {
		t.Fatalf("--folder --to-domain: code=%d stderr=%q; the folder value was read as the flag", code, stderr)
	}
}

func TestMailList_LimitTooLarge_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "list", "--limit", "1000", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "limit") {
		t.Errorf("stderr = %q, want mention of limit", stderr)
	}
}

func TestMailList_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "list", "--bogus"})

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

func TestMail_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "bogus"})

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

func TestMail_MissingAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should describe the missing action")
	}
}

func TestMailList_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "list", "--llm"})

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

func TestMailList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--llm"})

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

func TestMailList_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"mail", "list"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_MailList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"mail.list"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestCommands_LLM_ListsMailList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "mail", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"mail.list"`) {
		t.Errorf("stdout = %q, want mail.list listed", stdout)
	}
}

// TestMailList_ExactlyLimitMessages_NotTruncated is the regression test for a false
// "truncated" report: a folder holding exactly as many messages as --limit has nothing
// left to page through, so callers must not be told to ask for more.
func TestMailList_ExactlyLimitMessages_NotTruncated(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"7", "only message", []any{[]any{"mailbox", "noreply@mailbox.org"}}, 1784851574000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--limit", "1", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"truncated":false`) {
		t.Errorf("stdout = %q, want truncated:false", stdout)
	}
}

// TestMailList_MoreThanLimitMessages_TruncatedAndCapped pins the other side of the
// boundary: when the folder really does hold more than --limit messages the extra row is
// dropped from the output and truncated reports true.
func TestMailList_MoreThanLimitMessages_TruncatedAndCapped(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"7", "first", []any{[]any{"mailbox", "a@mailbox.org"}}, 1784851574000},
			[]any{"8", "second", []any{[]any{"mailbox", "b@mailbox.org"}}, 1784851574000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "list", "--limit", "1", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"truncated":true`) {
		t.Errorf("stdout = %q, want truncated:true", stdout)
	}
	if strings.Contains(stdout, "second") {
		t.Errorf("stdout = %q, want the row past --limit dropped", stdout)
	}
}
