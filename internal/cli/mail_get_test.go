// ABOUTME: CLI integration tests for the mail get command: human and LLM output,
// ABOUTME: validation, authentication failures, API failures, not-found, and stdout/stderr/exit-code separation.
package cli

import (
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestMailGet_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"7", "default0/INBOX", "mailbox invoice", [][]any{{"mailbox", "noreply@mailbox.org"}}, [][]any{{nil, "fixture-user@example.invalid"}}, []any{}, 165289, 1784851568000, 1784851574000, true, 40},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "get", "--id", "7", "--folder", "default0/INBOX"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "mailbox invoice") || !strings.Contains(stdout, "noreply@mailbox.org") {
		t.Errorf("stdout = %q, want subject and from present", stdout)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want login + mail get", len(reqs))
	}
	req := reqs[1]
	query := req.URL.Query()
	if req.Method != "GET" || req.URL.Path != "/ajax/mail" || len(query) != 5 ||
		query.Get("action") != "list" || query.Get("session") != "sess-1" || query.Get("timezone") != "UTC" ||
		query.Get("columns") != mailGetColumns ||
		query.Get("data") != `[{"folder":"default0/INBOX","id":"7"}]` {
		t.Errorf("mail get request = %s %s, want exact list query", req.Method, req.URL)
	}
}

func TestMailGet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"7", "default0/INBOX", "mailbox invoice", [][]any{{"mailbox", "noreply@mailbox.org"}}, [][]any{{nil, "fixture-user@example.invalid"}}, []any{}, 165289, 1784851568000, 1784851574000, true, 40},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "get", "--id", "7", "--folder", "default0/INBOX", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("stdout not JSON: %q", stdout)
	}
	if !strings.Contains(stdout, `"id":"7"`) || !strings.Contains(stdout, `"subject":"mailbox invoice"`) ||
		!strings.Contains(stdout, `"attachment":true,"seen":true,"flagged":true`) {
		t.Errorf("stdout = %q, want identity, subject, attachment, and decoded flag fields", stdout)
	}
	if strings.Contains(stdout, "Received:") || strings.Contains(stdout, "\"headers\"") {
		t.Errorf("stdout = %q, must not include raw headers/body content", stdout)
	}
}

func TestMailGet_LLMMode_PreservesAbsentDraftDates(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"6", "default0/Drafts", "unsent draft", []any{}, [][]any{{nil, "fixture-user@example.invalid"}}, []any{}, 128, nil, nil, false, 4},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "get", "--id", "6", "--folder", "default0/Drafts", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"sent_date":null,"received_date":null`) {
		t.Fatalf("stdout = %q, want absent draft dates preserved as null", stdout)
	}
}

func TestMailGet_EmptyCCRemainsSelectable(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"7", "default0/INBOX", "mailbox invoice", [][]any{{"mailbox", "noreply@mailbox.org"}}, [][]any{{nil, "fixture-user@example.invalid"}}, []any{}, 165289, 1784851568000, 1784851574000, true, 0},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "get", "--id", "7", "--folder", "default0/INBOX",
		"--format", "json", "--select", "cc",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != "{\"cc\":[]}\n" {
		t.Fatalf("stdout = %q, want selectable empty cc array", stdout)
	}
}

func TestMailGet_RejectsMismatchedResponseIdentity(t *testing.T) {
	tests := []struct {
		name     string
		response []any
	}{
		{
			name:     "wrong id",
			response: []any{"8", "default0/INBOX", "wrong", []any{}, []any{}, []any{}, 1, 1784851568000, 1784851574000, false, 0},
		},
		{
			name:     "wrong folder",
			response: []any{"7", "default0/Archive", "wrong", []any{}, []any{}, []any{}, 1, 1784851568000, 1784851574000, false, 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/mail", "list", testutil.Response{
				Body: map[string]any{"data": []any{tt.response}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"mail", "get", "--id", "7", "--folder", "default0/INBOX", "--llm",
			})

			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestMailGet_RejectsMultipleRows(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"7", "default0/INBOX", "requested", []any{}, []any{}, []any{}, 1, 1784851568000, 1784851574000, false, 0},
			{"8", "default0/INBOX", "unexpected", []any{}, []any{}, []any{}, 1, 1784851568000, 1784851574000, false, 0},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "get", "--id", "7", "--folder", "default0/INBOX", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailGet_NotFound(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "get", "--id", "999", "--folder", "default0/INBOX", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "not_found") {
		t.Errorf("stderr = %q, want not_found error code", stderr)
	}
}

func TestMailGet_RejectsNullResultCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mail", "get", "--id", "999", "--folder", "default0/INBOX", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want null collection rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response rather than not_found", stderr)
	}
}

func TestMailGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "get", "--folder", "default0/INBOX", "--llm"})

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

func TestMailGet_MissingFolder_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "get", "--id", "7", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--folder") {
		t.Errorf("stderr = %q, want mention of --folder", stderr)
	}
}

func TestMailGet_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "get", "--bogus"})

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

func TestMailGet_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"mail", "get", "--id", "7", "--folder", "default0/INBOX", "--llm"})

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

func TestMailGet_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "list", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mail", "get", "--id", "7", "--folder", "default0/INBOX", "--llm"})

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

func TestMailGet_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"mail", "get", "--id", "7", "--folder", "default0/INBOX"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_MailGet(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "mail.get", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"mail.get"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestCommands_LLM_ListsMailGet(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "mail", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"mail.get"`) {
		t.Errorf("stdout = %q, want mail.get listed", stdout)
	}
}
