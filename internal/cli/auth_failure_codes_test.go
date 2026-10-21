// ABOUTME: Tests that mailfilter and account commands report the real sign-in failure code.
// ABOUTME: Rate limits, server errors and missing credentials must not all become auth_failed (exit 2).
package cli

import (
	"net/http"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func mailfilterAndAccountCommands() map[string][]string {
	return map[string][]string{
		"mailfilter list":   {"mailfilter", "list", "--llm"},
		"mailfilter get":    {"mailfilter", "get", "--id", "2", "--llm"},
		"mailfilter create": {"mailfilter", "create", "--rule", `{"rulename":"r","actioncmds":[]}`, "--llm"},
		"mailfilter update": {"mailfilter", "update", "--id", "7", "--rule", `{"active":false}`, "--llm"},
		"mailfilter delete": {"mailfilter", "delete", "--id", "7", "--confirm", "7", "--llm"},
		"account list":      {"account", "list", "--llm"},
		"account get":       {"account", "get", "--id", "1", "--llm"},
		"account create": {"account", "create", "--login", "l", "--password", "p", "--mail-url", "imap://h:993",
			"--primary-address", "a@example.invalid", "--llm"},
		"account update": {"account", "update", "--id", "1", "--name", "n", "--llm"},
		"account delete": {"account", "delete", "--id", "1", "--confirm", "1", "--llm"},
	}
}

func TestMailfilterAndAccount_RateLimitedLoginIsNotAuthFailed(t *testing.T) {
	for name, args := range mailfilterAndAccountCommands() {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Status: http.StatusTooManyRequests, Body: map[string]any{}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, args)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1 for a rate-limited sign-in; stderr=%s", code, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, `"http_status":429`) || !strings.Contains(stderr, `"retryable":true`) {
				t.Errorf("stdout=%q stderr=%q, want the retryable 429 error", stdout, stderr)
			}
			if strings.Count(stderr, `"error"`) != 1 {
				t.Errorf("stderr = %q, want exactly one error document", stderr)
			}
		})
	}
}

func TestMailfilterAndAccount_MissingCredentialsIsInvalidArgument(t *testing.T) {
	for name, args := range mailfilterAndAccountCommands() {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code != 4 {
				t.Fatalf("exit code = %d, want 4 for missing credentials; stderr=%s", code, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, "invalid_argument") {
				t.Errorf("stdout=%q stderr=%q, want invalid_argument", stdout, stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("sent %d requests without credentials, want none", len(srv.Requests()))
			}
		})
	}
}

func TestMailfilterAndAccount_RejectedLoginStaysExitTwo(t *testing.T) {
	for name, args := range mailfilterAndAccountCommands() {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{
				Body: map[string]any{"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT"},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			_, stderr, code := run(t, srv, env, args)
			if code != 2 || !strings.Contains(stderr, "LGI-0006") {
				t.Fatalf("exit code = %d stderr=%s, want 2 with LGI-0006", code, stderr)
			}
		})
	}
}
