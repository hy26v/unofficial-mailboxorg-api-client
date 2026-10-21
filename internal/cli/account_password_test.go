// ABOUTME: Tests for the external account password sources of account create/update:
// ABOUTME: --password-stdin, --password-env (MBX_ACCOUNT_PASSWORD) and the --password warning.
package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func withAccountPasswordStdin(t *testing.T, input string) {
	t.Helper()
	previous := accountPasswordStdin
	accountPasswordStdin = strings.NewReader(input)
	t.Cleanup(func() { accountPasswordStdin = previous })
}

func accountRequestPassword(t *testing.T, srv *testutil.Server, action string) (string, bool) {
	t.Helper()
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/account" && req.URL.Query().Get("action") == action {
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode account %s body: %v", action, err)
			}
			password, ok := body["password"].(string)
			return password, ok
		}
	}
	t.Fatalf("no account %s request", action)
	return "", false
}

func accountPasswordServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/account", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 5}}})
	srv.Handle("/ajax/account", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 5}}})
	return srv
}

var accountCreateBase = []string{"account", "create", "--login", "foo", "--mail-url", "imap://host:993", "--primary-address", "foo@example.com"}

func TestAccountCreate_PasswordStdin(t *testing.T) {
	withAccountPasswordStdin(t, "s3cret pass\r\nsecond line ignored\n")
	srv := accountPasswordServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, append(append([]string{}, accountCreateBase...), "--password-stdin", "--llm"))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":5}`+"\n" || stderr != "" {
		t.Errorf("stdout=%q stderr=%q, want id only and no warning", stdout, stderr)
	}
	if password, _ := accountRequestPassword(t, srv, "new"); password != "s3cret pass" {
		t.Errorf("sent password = %q, want the first stdin line without its line ending", password)
	}
}

func TestAccountCreate_PasswordEnv(t *testing.T) {
	srv := accountPasswordServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw", "MBX_ACCOUNT_PASSWORD": "from-env"}
	_, stderr, code := run(t, srv, env, append(append([]string{}, accountCreateBase...), "--password-env", "--llm"))
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if password, _ := accountRequestPassword(t, srv, "new"); password != "from-env" {
		t.Errorf("sent password = %q, want MBX_ACCOUNT_PASSWORD", password)
	}
}

func TestAccountUpdate_PasswordStdin(t *testing.T) {
	withAccountPasswordStdin(t, "new-secret\n")
	srv := accountPasswordServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"account", "update", "--id", "5", "--password-stdin", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if password, _ := accountRequestPassword(t, srv, "update"); password != "new-secret" {
		t.Errorf("sent password = %q, want new-secret", password)
	}
}

func TestAccountUpdate_IgnoresPasswordEnvWithoutFlag(t *testing.T) {
	srv := accountPasswordServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw", "MBX_ACCOUNT_PASSWORD": "stale"}
	_, stderr, code := run(t, srv, env, []string{"account", "update", "--id", "5", "--name", "n", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if _, sent := accountRequestPassword(t, srv, "update"); sent {
		t.Error("account update sent a password from the environment without --password-env")
	}
}

func TestAccountPassword_FlagPrintsWarning(t *testing.T) {
	for name, structured := range map[string]bool{"human": false, "llm": true} {
		t.Run(name, func(t *testing.T) {
			srv := accountPasswordServer()
			defer srv.Close()

			args := append(append([]string{}, accountCreateBase...), "--password", "bar")
			if structured {
				args = append(args, "--llm")
			}
			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if !strings.Contains(stderr, "process list") || strings.Contains(stderr, "bar") {
				t.Errorf("stderr = %q, want a warning that does not repeat the password", stderr)
			}
			if structured && !strings.HasPrefix(stderr, `{"warning":{"code":"password_on_command_line"`) {
				t.Errorf("stderr = %q, want a structured warning", stderr)
			}
			if strings.Contains(stdout, "warning") {
				t.Errorf("stdout = %q, want the warning on stderr only", stdout)
			}
		})
	}
}

func TestAccountPassword_RejectsBadSourcesWithoutRequests(t *testing.T) {
	tests := map[string]struct {
		args  []string
		env   map[string]string
		stdin string
		want  string
	}{
		"two sources":    {args: []string{"--password", "x", "--password-stdin"}, stdin: "y\n", want: "only one of"},
		"empty env":      {args: []string{"--password-env"}, want: "MBX_ACCOUNT_PASSWORD"},
		"empty stdin":    {args: []string{"--password-stdin"}, stdin: "\n", want: "did not contain a password"},
		"no source":      {args: nil, want: "a password is required"},
		"too long stdin": {args: []string{"--password-stdin"}, stdin: strings.Repeat("a", maxAccountPasswordBytes+1) + "\n", want: "longer than"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			withAccountPasswordStdin(t, tc.stdin)
			srv := accountPasswordServer()
			defer srv.Close()
			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			for key, value := range tc.env {
				env[key] = value
			}
			args := append(append(append([]string{}, accountCreateBase...), tc.args...), "--llm")
			stdout, stderr, code := run(t, srv, env, args)
			if code != 4 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Errorf("code=%d stdout=%q stderr=%q, want invalid_argument containing %q", code, stdout, stderr, tc.want)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("sent %d requests, want none", len(srv.Requests()))
			}
		})
	}
}
