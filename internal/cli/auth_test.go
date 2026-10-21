// ABOUTME: CLI integration tests for the login and logout commands: human and LLM output,
// ABOUTME: dry-run, missing credentials, API failures, and stdout/stderr/exit-code separation.
package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/config"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestLogin_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"login"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "fixture-user@example.invalid") {
		t.Errorf("stdout = %q, want username present", stdout)
	}
	if strings.Contains(stdout, "sess-1") {
		t.Errorf("stdout = %q, must not leak session id", stdout)
	}
}

func TestLogin_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"login", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if stdout != `{"status":"ok","username":"fixture-user@example.invalid"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestLogin_PersistsSessionForSubsequentCommands(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"email1": "fixture-user@example.invalid"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	sessionPath := filepath.Join(t.TempDir(), "session.json")
	getenv := func(k string) string { return env[k] }

	code1 := runWith(t, srv, sessionPath, getenv, []string{"login"})
	if code1 != 0 {
		t.Fatalf("login exit code = %d", code1)
	}

	store := &client.SessionStore{Path: sessionPath}
	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sess.ID != "sess-1" {
		t.Errorf("persisted session id = %q, want sess-1", sess.ID)
	}
}

func TestLogin_InteractivePromptSuppliesMissingPassword(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	promptCalls := 0
	stdout, stderr, code := runWithConfigAndPasswordPrompt(
		t,
		srv,
		map[string]string{},
		[]string{"login"},
		`username = "fixture-user@example.invalid"`,
		func() (string, error) {
			promptCalls++
			return "prompted-secret", nil
		},
	)

	if code != 0 || stderr != "" {
		t.Fatalf("prompted login: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if promptCalls != 1 {
		t.Fatalf("prompt calls = %d, want 1", promptCalls)
	}
	requests := srv.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if got := requests[0].Form.Get("password"); got != "prompted-secret" {
		t.Fatalf("submitted password = %q, want prompted value", got)
	}
}

func TestLogin_InteractiveFirstRunPromptsAndPersistsUsernameBeforePassword(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	usernameCalls := 0
	passwordCalls := 0
	stdout, stderr, code, configPath := runWithConfigAndLoginPrompts(
		t,
		srv,
		map[string]string{},
		[]string{"login"},
		"[safety]\ndelete_protection = [\"all\"]\n",
		LoginPrompts{
			Username: func() (string, error) {
				usernameCalls++
				if passwordCalls != 0 {
					t.Fatal("password was prompted before username")
				}
				return "  first-run@example.org  ", nil
			},
			Password: func() (string, error) {
				passwordCalls++
				return "prompted-secret", nil
			},
		},
	)

	if code != 0 || stderr != "" {
		t.Fatalf("first-run login: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if usernameCalls != 1 || passwordCalls != 1 {
		t.Fatalf("prompt calls = username:%d password:%d, want 1 each", usernameCalls, passwordCalls)
	}
	settings, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load prompted config failed: %v", err)
	}
	if settings.Username != "first-run@example.org" {
		t.Fatalf("saved username = %q", settings.Username)
	}
	if got := strings.Join(settings.Safety.DeleteProtection, ","); got != "all" {
		t.Fatalf("delete protection = %q, want all", got)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat prompted config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read prompted config: %v", err)
	}
	if strings.Contains(string(configBytes), "prompted-secret") {
		t.Fatal("prompted password was written to config")
	}
	requests := srv.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if got := requests[0].Form.Get("name"); got != "first-run@example.org" {
		t.Fatalf("submitted username = %q", got)
	}
	if got := requests[0].Form.Get("password"); got != "prompted-secret" {
		t.Fatalf("submitted password = %q", got)
	}
}

func TestLogin_PersistedSessionSupportsPasswordlessWhoami(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"session": "credential-session"},
		Cookies: []*http.Cookie{{
			Name: "remember", Value: "cookie-1", Path: "/", MaxAge: 3600,
		}},
	})
	srv.Handle("/ajax/login", "autologin", testutil.Response{
		Body: map[string]any{"session": "autologin-session"},
	})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"email1": "first-run@example.org", "display_name": "First Run", "user_id": 42,
		}},
	})

	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	sessionPath := filepath.Join(root, "session.json")
	getenv := func(string) string { return "" }
	var stdout, stderr strings.Builder

	code := RunWithLoginPrompts(
		&stdout,
		&stderr,
		[]string{"login", "--llm"},
		srv.Client(),
		srv.URL+"/ajax",
		configPath,
		sessionPath,
		getenv,
		LoginPrompts{
			Username: func() (string, error) { return "first-run@example.org", nil },
			Password: func() (string, error) { return "prompted-secret", nil },
		},
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("interactive login: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(
		&stdout,
		&stderr,
		[]string{"whoami", "--llm"},
		srv.Client(),
		srv.URL+"/ajax",
		configPath,
		sessionPath,
		getenv,
	)
	if code != 0 || stderr.Len() != 0 {
		t.Fatalf("passwordless whoami: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.String() != `{"email":"first-run@example.org","display_name":"First Run","user_id":42}`+"\n" {
		t.Fatalf("passwordless whoami stdout = %q", stdout.String())
	}

	requests := srv.Requests()
	wantActions := []string{"login", "autologin", "get"}
	if len(requests) != len(wantActions) {
		t.Fatalf("request count = %d, want %d", len(requests), len(wantActions))
	}
	for i, want := range wantActions {
		if got := requests[i].URL.Query().Get("action"); got != want {
			t.Fatalf("request %d action = %q, want %q", i, got, want)
		}
	}
}

func TestLogin_ConfiguredUsernameSkipsUsernamePrompt(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	usernameCalls := 0
	passwordCalls := 0
	_, stderr, code, _ := runWithConfigAndLoginPrompts(
		t,
		srv,
		map[string]string{},
		[]string{"login"},
		`username = "configured@example.org"`,
		LoginPrompts{
			Username: func() (string, error) {
				usernameCalls++
				return "must-not-be-used@example.org", nil
			},
			Password: func() (string, error) {
				passwordCalls++
				return "prompted-secret", nil
			},
		},
	)

	if code != 0 || stderr != "" {
		t.Fatalf("configured login: code=%d stderr=%q", code, stderr)
	}
	if usernameCalls != 0 || passwordCalls != 1 {
		t.Fatalf("prompt calls = username:%d password:%d, want 0 and 1", usernameCalls, passwordCalls)
	}
}

func TestLogin_InvalidPromptedUsernameStopsBeforePasswordOrNetwork(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	passwordCalls := 0
	_, stderr, code, configPath := runWithConfigAndLoginPrompts(
		t,
		srv,
		map[string]string{},
		[]string{"login"},
		"",
		LoginPrompts{
			Username: func() (string, error) { return "not-an-email", nil },
			Password: func() (string, error) {
				passwordCalls++
				return "must-not-be-used", nil
			},
		},
	)

	if code == 0 || !strings.Contains(stderr, "must contain one @") {
		t.Fatalf("invalid username: code=%d stderr=%q", code, stderr)
	}
	if passwordCalls != 0 {
		t.Fatalf("password prompt calls = %d, want 0", passwordCalls)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("requests = %d, want 0", len(srv.Requests()))
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config exists after invalid username: %v", err)
	}
}

func TestLogin_DryRunDoesNotPerformInteractiveFirstRunSetup(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	usernameCalls := 0
	passwordCalls := 0
	_, stderr, code, configPath := runWithConfigAndLoginPrompts(
		t,
		srv,
		map[string]string{},
		[]string{"login", "--dry-run"},
		"",
		LoginPrompts{
			Username: func() (string, error) {
				usernameCalls++
				return "must-not-be-used@example.org", nil
			},
			Password: func() (string, error) {
				passwordCalls++
				return "must-not-be-used", nil
			},
		},
	)

	if code == 0 || !strings.Contains(stderr, "config.toml") {
		t.Fatalf("dry-run without config: code=%d stderr=%q", code, stderr)
	}
	if usernameCalls != 0 || passwordCalls != 0 {
		t.Fatalf("prompt calls = username:%d password:%d, want 0", usernameCalls, passwordCalls)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config exists after dry-run: %v", err)
	}
}

func TestLogin_EnvironmentPasswordSkipsInteractivePrompt(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	promptCalls := 0
	_, stderr, code := runWithConfigAndPasswordPrompt(
		t,
		srv,
		map[string]string{"MBX_PASSWORD": "environment-secret"},
		[]string{"login"},
		`username = "fixture-user@example.invalid"`,
		func() (string, error) {
			promptCalls++
			return "wrong-source", nil
		},
	)

	if code != 0 || stderr != "" {
		t.Fatalf("environment login: code=%d stderr=%q", code, stderr)
	}
	if promptCalls != 0 {
		t.Fatalf("prompt calls = %d, want 0", promptCalls)
	}
	requests := srv.Requests()
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	if got := requests[0].Form.Get("password"); got != "environment-secret" {
		t.Fatalf("submitted password = %q, want environment value", got)
	}
}

func TestLogin_MissingUsernameDoesNotPromptForPassword(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	promptCalls := 0
	_, stderr, code := runWithConfigAndPasswordPrompt(
		t,
		srv,
		map[string]string{},
		[]string{"login"},
		"",
		func() (string, error) {
			promptCalls++
			return "must-not-be-used", nil
		},
	)

	if code == 0 || !strings.Contains(stderr, "config.toml") {
		t.Fatalf("missing-username login: code=%d stderr=%q", code, stderr)
	}
	if promptCalls != 0 {
		t.Fatalf("prompt calls = %d, want 0", promptCalls)
	}
}

func TestLogin_DryRunNeedsNoPasswordAndDoesNotPrompt(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	promptCalls := 0
	stdout, stderr, code := runWithConfigAndPasswordPrompt(
		t,
		srv,
		map[string]string{},
		[]string{"login", "--dry-run"},
		`username = "fixture-user@example.invalid"`,
		func() (string, error) {
			promptCalls++
			return "must-not-be-used", nil
		},
	)

	if code != 0 || stderr != "" {
		t.Fatalf("dry-run login: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if promptCalls != 0 {
		t.Fatalf("prompt calls = %d, want 0", promptCalls)
	}
	if !strings.Contains(stdout, "fixture-user@example.invalid") {
		t.Fatalf("stdout = %q, want configured username", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry-run requests = %d, want 0", len(srv.Requests()))
	}
}

func TestLogin_InvalidArgumentsDoNotPrompt(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	usernameCalls := 0
	passwordCalls := 0
	_, stderr, code, _ := runWithConfigAndLoginPrompts(
		t,
		srv,
		map[string]string{},
		[]string{"login", "--unknown"},
		"",
		LoginPrompts{
			Username: func() (string, error) {
				usernameCalls++
				return "must-not-be-used@example.org", nil
			},
			Password: func() (string, error) {
				passwordCalls++
				return "must-not-be-used", nil
			},
		},
	)

	if code == 0 || !strings.Contains(stderr, "unknown flag") {
		t.Fatalf("invalid login: code=%d stderr=%q", code, stderr)
	}
	if usernameCalls != 0 || passwordCalls != 0 {
		t.Fatalf("prompt calls = username:%d password:%d, want 0", usernameCalls, passwordCalls)
	}
}

func TestLogin_PromptFailureIsStructuredAndDoesNotContactServer(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := runWithConfigAndPasswordPrompt(
		t,
		srv,
		map[string]string{},
		[]string{"login", "--llm"},
		`username = "fixture-user@example.invalid"`,
		func() (string, error) { return "", errors.New("terminal input canceled") },
	)

	if code == 0 || stdout != "" {
		t.Fatalf("prompt failure: code=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stderr, `"code":"invalid_argument"`) || !strings.Contains(stderr, "terminal input canceled") {
		t.Fatalf("stderr = %q, want structured prompt error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("prompt failure requests = %d, want 0", len(srv.Requests()))
	}
}

func TestLogin_MissingCredentials_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"login", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "config.toml") || !strings.Contains(stderr, "MBX_PASSWORD") {
		t.Errorf("stderr = %q, want mention of missing credentials", stderr)
	}
}

func TestLogin_APIFailure_AuthFailedError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "The user name or password is incorrect.",
			"code":  "LGI-0006", "categories": "USER_INPUT", "error_id": "req-1",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "wrong"}
	stdout, stderr, code := run(t, srv, env, []string{"login", "--llm"})

	if code != 2 {
		t.Fatalf("exit code = %d, want documented authentication exit code 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "LGI-0006") {
		t.Errorf("stderr = %q, want error code present", stderr)
	}
}

func TestLogin_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"login", "--dry-run"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "fixture-user@example.invalid") {
		t.Errorf("stdout = %q, want username present", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
}

func TestLogout_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "logout", testutil.Response{Body: []byte{}})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{ID: "sess-1"}); err != nil {
		t.Fatalf("priming session store failed: %v", err)
	}

	getenv := func(k string) string { return "" }
	stdout, stderr, code := runWithOutput(t, srv, sessionPath, getenv, []string{"logout"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "logged out") {
		t.Errorf("stdout = %q, want confirmation", stdout)
	}

	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sess.ID != "" {
		t.Errorf("session id = %q, want cleared", sess.ID)
	}
}

func TestLogout_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "logout", testutil.Response{Body: []byte{}})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{ID: "sess-1"}); err != nil {
		t.Fatalf("priming session store failed: %v", err)
	}

	getenv := func(k string) string { return "" }
	stdout, stderr, code := runWithOutput(t, srv, sessionPath, getenv, []string{"logout", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"status":"ok"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestLogout_NoStoredSession_SucceedsWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{}
	stdout, stderr, code := run(t, srv, env, []string{"logout"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "no active session") {
		t.Errorf("stdout = %q, want no-active-session message", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want 0", len(srv.Requests()))
	}
}

func TestLogout_ClearsCookieOnlyStoredSession(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{
		Cookies: []*http.Cookie{{Name: "JSESSIONID", Value: "autologin-secret"}},
	}); err != nil {
		t.Fatalf("priming cookie-only session store failed: %v", err)
	}

	stdout, stderr, code := runWithOutput(t, srv, sessionPath, func(string) string { return "" }, []string{"logout"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "cleared local session") {
		t.Fatalf("stdout = %q, want local-session confirmation", stdout)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("requests = %d, want 0 without a server session id", got)
	}
	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if len(sess.Cookies) != 0 {
		t.Fatalf("stored cookies = %+v, want cleared", sess.Cookies)
	}
}

func TestLogout_DryRun_WithStoredSession_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{ID: "sess-1"}); err != nil {
		t.Fatalf("priming session store failed: %v", err)
	}

	getenv := func(k string) string { return "" }
	stdout, stderr, code := runWithOutput(t, srv, sessionPath, getenv, []string{"logout", "--dry-run"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
	if !strings.Contains(stdout, "dry run") {
		t.Errorf("stdout = %q, want dry-run message", stdout)
	}
	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sess.ID != "sess-1" {
		t.Errorf("dry-run must not clear stored session, got %q", sess.ID)
	}
}

func TestLogout_APIFailure_DoesNotClearLocalSession(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "logout", testutil.Response{
		Body: map[string]any{
			"error": "boom", "code": "SVL-0011", "categories": "ERROR", "error_id": "req-5",
		},
	})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{ID: "sess-1"}); err != nil {
		t.Fatalf("priming session store failed: %v", err)
	}

	getenv := func(k string) string { return "" }
	_, stderr, code := runWithOutput(t, srv, sessionPath, getenv, []string{"logout", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if !strings.Contains(stderr, "SVL-0011") {
		t.Errorf("stderr = %q, want error code present", stderr)
	}

	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sess.ID != "sess-1" {
		t.Errorf("session id = %q, want unchanged after API failure", sess.ID)
	}
}

func TestLogout_LoginRateLimitDoesNotClearLocalSession(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "logout", testutil.Response{
		Body: map[string]any{"error": "Too many login attempts", "code": "LGI-0026"},
	})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{ID: "sess-1"}); err != nil {
		t.Fatalf("priming session store failed: %v", err)
	}

	_, stderr, code := runWithOutput(t, srv, sessionPath, func(string) string { return "" }, []string{"logout", "--llm"})
	if code == 0 || !strings.Contains(stderr, "LGI-0026") {
		t.Fatalf("logout during a login rate limit: code=%d stderr=%q; want the rate-limit error", code, stderr)
	}
	if sess, err := store.Load(); err != nil || sess.ID != "sess-1" {
		t.Fatalf("stored session = %q, %v; want it kept (a rate limit does not prove the session is invalid)", sess.ID, err)
	}
}

func TestLogout_ExpiredProviderSessionClearsLocalState(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "logout", testutil.Response{
		Body: map[string]any{
			"error": "The session is not valid.", "code": "SES-0203", "categories": "USER_INPUT",
		},
	})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	store := &client.SessionStore{Path: sessionPath}
	if err := store.Save(client.Session{ID: "expired-session"}); err != nil {
		t.Fatalf("priming session store failed: %v", err)
	}

	stdout, stderr, code := runWithOutput(t, srv, sessionPath, func(string) string { return "" }, []string{"logout", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want idempotent success; stdout=%s stderr=%s", code, stdout, stderr)
	}
	sess, err := store.Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if sess.ID != "" || len(sess.Cookies) != 0 {
		t.Fatalf("stored session = %+v, want cleared after definitive expiry", sess)
	}
}

func TestAuthenticationLifecycle_PersistsReusesClearsAndReauthenticates(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	setSuccessfulLogin := func(sessionID, cookieValue string) {
		srv.Handle("/ajax/login", "login", testutil.Response{
			Body: map[string]any{"session": sessionID},
			Cookies: []*http.Cookie{{
				Name: "remember", Value: cookieValue, Path: "/", MaxAge: 3600,
			}},
		})
	}
	setSuccessfulAutologin := func(sessionID, cookieValue string) {
		srv.Handle("/ajax/login", "autologin", testutil.Response{
			Body: map[string]any{"session": sessionID},
			Cookies: []*http.Cookie{{
				Name: "remember", Value: cookieValue, Path: "/", MaxAge: 3600,
			}},
		})
	}
	setSuccessfulLogin("credential-session-1", "cookie-1")
	setSuccessfulAutologin("autologin-session-1", "cookie-auto-1")
	srv.Handle("/ajax/login", "logout", testutil.Response{Body: []byte{}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"email1": "fixture-user@example.invalid", "display_name": "CLI Test", "user_id": 42,
		}},
	})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	credentials := func(password string) func(string) string {
		return func(key string) string {
			switch key {
			case "MBX_USERNAME":
				return "fixture-user@example.invalid"
			case "MBX_PASSWORD":
				return password
			default:
				return ""
			}
		}
	}
	runCommand := func(password string, args ...string) (string, string, int) {
		t.Helper()
		return runWithOutput(t, srv, sessionPath, credentials(password), args)
	}

	stdout, stderr, code := runCommand("correct-password", "login", "--llm")
	if code != 0 || stderr != "" {
		t.Fatalf("initial login: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout != `{"status":"ok","username":"fixture-user@example.invalid"}`+"\n" {
		t.Fatalf("initial login stdout = %q", stdout)
	}
	info, err := os.Stat(sessionPath)
	if err != nil {
		t.Fatalf("stat persisted session: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() == 0 {
		t.Fatalf("persisted session mode/size = %v/%d, want regular 0600 non-empty file", info.Mode(), info.Size())
	}

	// A separate CLI invocation has no password at all. It must authenticate entirely
	// through the persisted cookie created by explicit login.
	stdout, stderr, code = runCommand("", "whoami", "--llm")
	if code != 0 || stderr != "" {
		t.Fatalf("stored-session reuse: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if stdout != `{"email":"fixture-user@example.invalid","display_name":"CLI Test","user_id":42}`+"\n" {
		t.Fatalf("stored-session whoami stdout = %q", stdout)
	}

	stdout, stderr, code = runCommand("deliberately-wrong", "logout", "--llm")
	if code != 0 || stderr != "" || stdout != `{"status":"ok"}`+"\n" {
		t.Fatalf("first logout: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Lstat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("session store after logout: err=%v, want missing file", err)
	}

	// With the store cleared, the same rejected password must reach credential login and
	// fail. This distinguishes local file removal from merely printing logout success.
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{
		"error": "The user name or password is incorrect.",
		"code":  "LGI-0006", "categories": "USER_INPUT", "error_id": "req-auth-lifecycle",
	}})
	stdout, stderr, code = runCommand("deliberately-wrong", "whoami", "--llm")
	if code != 2 || stdout != "" || !strings.Contains(stderr, `"code":"LGI-0006"`) {
		t.Fatalf("post-logout authentication: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Lstat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("failed post-logout login recreated session store: err=%v", err)
	}

	setSuccessfulLogin("credential-session-2", "cookie-2")
	setSuccessfulAutologin("autologin-session-2", "cookie-auto-2")
	stdout, stderr, code = runCommand("correct-password", "login", "--llm")
	if code != 0 || stderr != "" || stdout != `{"status":"ok","username":"fixture-user@example.invalid"}`+"\n" {
		t.Fatalf("fresh login: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runCommand("", "whoami", "--llm")
	if code != 0 || stderr != "" || stdout != `{"email":"fixture-user@example.invalid","display_name":"CLI Test","user_id":42}`+"\n" {
		t.Fatalf("fresh-session reuse: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	stdout, stderr, code = runCommand("deliberately-wrong", "logout", "--llm")
	if code != 0 || stderr != "" || stdout != `{"status":"ok"}`+"\n" {
		t.Fatalf("final logout: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, err := os.Lstat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("session store after final logout: err=%v, want missing file", err)
	}

	requests := srv.Requests()
	wantActions := []string{"login", "autologin", "get", "logout", "login", "login", "autologin", "get", "logout"}
	wantMethods := []string{
		http.MethodPost, http.MethodGet, http.MethodGet, http.MethodGet, http.MethodPost,
		http.MethodPost, http.MethodGet, http.MethodGet, http.MethodGet,
	}
	if len(requests) != len(wantActions) {
		t.Fatalf("request count = %d, want %d", len(requests), len(wantActions))
	}
	for i, req := range requests {
		if got := req.URL.Query().Get("action"); got != wantActions[i] {
			t.Errorf("request %d action = %q, want %q", i, got, wantActions[i])
		}
		if req.Method != wantMethods[i] {
			t.Errorf("request %d method = %q, want %q", i, req.Method, wantMethods[i])
		}
	}
	if got := requests[0].Form.Get("password"); got != "correct-password" {
		t.Errorf("initial credential password = %q, want correct-password", got)
	}
	if got := requests[4].Form.Get("password"); got != "deliberately-wrong" {
		t.Errorf("post-logout credential password = %q, want deliberately-wrong", got)
	}
	if got := requests[5].Form.Get("password"); got != "correct-password" {
		t.Errorf("fresh credential password = %q, want correct-password", got)
	}
	if got := requests[2].URL.Query().Get("session"); got != "autologin-session-1" {
		t.Errorf("first whoami session = %q, want autologin-session-1", got)
	}
	if got := requests[3].URL.Query().Get("session"); got != "autologin-session-1" {
		t.Errorf("first logout session = %q, want autologin-session-1", got)
	}
	if got := requests[7].URL.Query().Get("session"); got != "autologin-session-2" {
		t.Errorf("fresh whoami session = %q, want autologin-session-2", got)
	}
	if got := requests[8].URL.Query().Get("session"); got != "autologin-session-2" {
		t.Errorf("final logout session = %q, want autologin-session-2", got)
	}
}

func TestChangePassword_MissingConfirm_ConfirmationRequiredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_argument") {
		t.Errorf("stderr = %q, want invalid_argument error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want 0 without confirmation", len(srv.Requests()))
	}
}

func TestChangePassword_ConfirmMismatch_ConfirmationRequiredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--confirm", "wrong@mailbox.org", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_argument") {
		t.Errorf("stderr = %q, want invalid_argument error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want 0 on confirm mismatch", len(srv.Requests()))
	}
}

func TestChangePassword_MissingCurrentPassword_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--new-password", "new-pw", "--confirm", "fixture-user@example.invalid", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "MBX_PASSWORD") || !strings.Contains(stderr, "--current-password") {
		t.Errorf("stderr = %q, want guidance naming MBX_PASSWORD and --current-password", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want 0 without a current password", len(srv.Requests()))
	}
}

func TestChangePassword_MissingNewPassword_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--confirm", "fixture-user@example.invalid", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if !strings.Contains(stderr, "--new-password") {
		t.Errorf("stderr = %q, want mention of --new-password", stderr)
	}
}

func TestChangePassword_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--confirm", "fixture-user@example.invalid", "--dry-run"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "Warning: "+changePasswordArgsWarning+"\n" {
		t.Errorf("stderr = %q, want only the password-argument warning", stderr)
	}
	if !strings.Contains(stdout, "dry run") && !strings.Contains(stdout, "Dry run") {
		t.Errorf("stdout = %q, want dry-run notice", stdout)
	}
	if strings.Contains(stdout, "new-pw") {
		t.Errorf("stdout = %q, must not leak new password", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
}

func TestChangePassword_DryRun_DoesNotRequireLoginPassword(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--confirm", "fixture-user@example.invalid", "--dry-run"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "Warning: "+changePasswordArgsWarning+"\n" {
		t.Errorf("stderr = %q, want only the password-argument warning", stderr)
	}
	if !strings.Contains(stdout, "dry run") && !strings.Contains(stdout, "Dry run") {
		t.Errorf("stdout = %q, want dry-run notice", stdout)
	}
	if strings.Contains(stdout, "pw") {
		t.Errorf("stdout = %q, must not leak either password", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
}

func TestChangePassword_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/passwordchange", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--confirm", "fixture-user@example.invalid"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "Warning: "+changePasswordArgsWarning+"\n" {
		t.Errorf("stderr = %q, want only the password-argument warning", stderr)
	}
	if strings.Contains(stdout, "new-pw") || strings.Contains(stdout, "pw\n") {
		t.Errorf("stdout = %q, must not leak passwords", stdout)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("made %d requests, want 2 (login + passwordchange)", len(reqs))
	}
	last := reqs[len(reqs)-1]
	if last.URL.Path != "/ajax/passwordchange" {
		t.Errorf("request path = %q, want /ajax/passwordchange", last.URL.Path)
	}
	if last.Method != http.MethodPut {
		t.Errorf("request method = %q, want PUT", last.Method)
	}
	if last.URL.Query().Get("action") != "update" {
		t.Errorf("request action = %q, want update", last.URL.Query().Get("action"))
	}
	var passwordBody map[string]string
	if err := json.NewDecoder(last.Body).Decode(&passwordBody); err != nil {
		t.Fatalf("decode password-change request body: %v", err)
	}
	if len(passwordBody) != 2 {
		t.Errorf("password-change request has %d fields, want 2", len(passwordBody))
	}
	if passwordBody["old_password"] != "pw" {
		t.Error("old_password field did not match the supplied current password")
	}
	if passwordBody["new_password"] != "new-pw" {
		t.Error("new_password field did not match the supplied new password")
	}
}

func TestChangePassword_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/passwordchange", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--confirm", "fixture-user@example.invalid", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"status":"ok"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestChangePassword_APIFailure_ErrorMapped(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/passwordchange", "update", testutil.Response{
		Body: map[string]any{
			"error": "Password was not changed.",
			"code":  "PSW-0004", "categories": "USER_INPUT", "error_desc": "Missing parameter new_password",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--confirm", "fixture-user@example.invalid", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "PSW-0004") {
		t.Errorf("stderr = %q, want error code present", stderr)
	}
}

// runWith runs the CLI with an explicit sessionPath (rather than a fresh one), returning
// only the exit code, for tests that need to inspect the persisted session store afterward.
func runWith(t *testing.T, srv *testutil.Server, sessionPath string, getenv func(string) string, args []string) int {
	_, _, code := runWithOutput(t, srv, sessionPath, getenv, args)
	return code
}

func runWithOutput(t *testing.T, srv *testutil.Server, sessionPath string, getenv func(string) string, args []string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut strings.Builder
	configPath := filepath.Join(t.TempDir(), "config.toml")
	code = Run(&out, &errOut, args, srv.Client(), srv.URL+"/ajax", configPath, sessionPath, getenv)
	return out.String(), errOut.String(), code
}

func TestWhoami_RefusesSessionCreatedForAnotherAccount(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body:    map[string]any{"session": "first-session"},
		Cookies: []*http.Cookie{{Name: "remember", Value: "first-cookie", Path: "/", MaxAge: 3600}},
	})
	srv.Handle("/ajax/login", "autologin", testutil.Response{Body: map[string]any{"session": "must-not-autologin"}})
	srv.Handle("/ajax/user", "get", testutil.Response{Body: map[string]any{"data": map[string]any{"email1": "first@example.org"}}})

	sessionPath := filepath.Join(t.TempDir(), "session.json")
	first := func(key string) string {
		return map[string]string{"MBX_USERNAME": "first@example.org", "MBX_PASSWORD": "pw"}[key]
	}
	if code := runWith(t, srv, sessionPath, first, []string{"login"}); code != 0 {
		t.Fatalf("login exit code = %d", code)
	}

	second := func(key string) string {
		return map[string]string{"MBX_USERNAME": "second@example.org"}[key]
	}
	_, stderr, code := runWithOutput(t, srv, sessionPath, second, []string{"whoami", "--llm"})
	if code != 2 || !strings.Contains(stderr, "different account") || !strings.Contains(stderr, "mbx login") {
		t.Fatalf("whoami for another account: code=%d stderr=%q; want auth_failed with mbx login guidance", code, stderr)
	}
	for _, req := range srv.Requests() {
		if action := req.URL.Query().Get("action"); action != "login" {
			t.Fatalf("unexpected %q request with another account's session", action)
		}
	}
}

func changePasswordServer(t *testing.T) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	t.Cleanup(srv.Close)
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/passwordchange", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})
	return srv
}

func passwordChangeBody(t *testing.T, srv *testutil.Server) map[string]string {
	t.Helper()
	reqs := srv.Requests()
	if len(reqs) == 0 || reqs[len(reqs)-1].URL.Path != "/ajax/passwordchange" {
		t.Fatalf("requests = %d, want a final password change", len(reqs))
	}
	var body map[string]string
	if err := json.NewDecoder(reqs[len(reqs)-1].Body).Decode(&body); err != nil {
		t.Fatalf("decode password-change body: %v", err)
	}
	return body
}

func TestChangePassword_EnvironmentInputNeedsNoPasswordArguments(t *testing.T) {
	srv := changePasswordServer(t)
	env := map[string]string{
		"MBX_USERNAME":     "fixture-user@example.invalid",
		"MBX_PASSWORD":     "env-current",
		"MBX_NEW_PASSWORD": "env-new",
	}
	stdout, stderr, code := run(t, srv, env, []string{"change-password", "--confirm", "fixture-user@example.invalid", "--llm"})
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want success without a warning", code, stderr)
	}
	if stdout != `{"status":"ok"}`+"\n" {
		t.Fatalf("stdout = %q", stdout)
	}
	body := passwordChangeBody(t, srv)
	if body["old_password"] != "env-current" || body["new_password"] != "env-new" {
		t.Fatal("password-change body did not use MBX_PASSWORD and MBX_NEW_PASSWORD")
	}
}

func TestChangePassword_MissingNewPasswordNamesEnvironmentInput(t *testing.T) {
	srv := changePasswordServer(t)
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"change-password", "--confirm", "fixture-user@example.invalid", "--llm"})
	if code != 4 || !strings.Contains(stderr, "MBX_NEW_PASSWORD") {
		t.Fatalf("code=%d stderr=%q, want invalid_argument naming MBX_NEW_PASSWORD", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("made %d requests, want 0", len(srv.Requests()))
	}
}

func runChangePasswordWithSecretPrompt(t *testing.T, srv *testutil.Server, env map[string]string, args []string, prompt SecretPrompt) (string, string, int) {
	t.Helper()
	var stdout, stderr strings.Builder
	root := t.TempDir()
	code := RunWithLoginPrompts(&stdout, &stderr, args, srv.Client(), srv.URL+"/ajax",
		filepath.Join(root, "config.toml"), filepath.Join(root, "session.json"),
		func(key string) string { return env[key] }, LoginPrompts{Secret: prompt})
	return stdout.String(), stderr.String(), code
}

func TestChangePassword_TerminalPromptAsksForMissingPasswords(t *testing.T) {
	srv := changePasswordServer(t)
	answers := map[string]string{
		"Current mailbox.org password: ":        "typed-current",
		"New mailbox.org password: ":            "typed-new",
		"Repeat the new mailbox.org password: ": "typed-new",
	}
	var labels []string
	prompt := func(label string) (string, error) {
		labels = append(labels, label)
		return answers[label], nil
	}
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid"}
	stdout, stderr, code := runChangePasswordWithSecretPrompt(t, srv, env, []string{"change-password", "--confirm", "fixture-user@example.invalid"}, prompt)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if len(labels) != 3 {
		t.Fatalf("prompt labels = %q, want current, new and repeat", labels)
	}
	body := passwordChangeBody(t, srv)
	if body["old_password"] != "typed-current" || body["new_password"] != "typed-new" {
		t.Fatal("password-change body did not use the prompted values")
	}
	if got := srv.Requests()[0].FormValue("password"); got != "typed-current" {
		t.Fatal("credential fallback did not use the prompted current password")
	}
}

func TestChangePassword_TerminalPromptMismatchSendsNothing(t *testing.T) {
	srv := changePasswordServer(t)
	answers := map[string]string{"New mailbox.org password: ": "first", "Repeat the new mailbox.org password: ": "second"}
	prompt := func(label string) (string, error) { return answers[label], nil }
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := runChangePasswordWithSecretPrompt(t, srv, env, []string{"change-password", "--confirm", "fixture-user@example.invalid"}, prompt)
	if code != 4 || !strings.Contains(stderr, "do not match") {
		t.Fatalf("code=%d stderr=%q, want a mismatch error", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("made %d requests, want 0", len(srv.Requests()))
	}
}

func TestChangePassword_TerminalPromptNotUsedForInvalidOrDryRunInvocations(t *testing.T) {
	for name, args := range map[string][]string{
		"missing confirm":  {"change-password"},
		"wrong confirm":    {"change-password", "--confirm", "other@example.invalid"},
		"dry run":          {"change-password", "--confirm", "fixture-user@example.invalid", "--dry-run"},
		"unknown argument": {"change-password", "--confirm", "fixture-user@example.invalid", "--bogus"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := changePasswordServer(t)
			prompt := func(label string) (string, error) {
				t.Fatalf("prompted %q for an invocation that sends nothing", label)
				return "", nil
			}
			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid"}
			runChangePasswordWithSecretPrompt(t, srv, env, args, prompt)
			if len(srv.Requests()) != 0 {
				t.Fatalf("made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

func TestChangePassword_PasswordArgumentWarningUsesTheOutputMode(t *testing.T) {
	for name, structured := range map[string]bool{"human": false, "llm": true} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			args := []string{"change-password", "--current-password", "pw", "--new-password", "new-pw", "--confirm", "fixture-user@example.invalid", "--dry-run"}
			if structured {
				args = append(args, "--llm")
			}
			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid"}
			stdout, stderr, code := run(t, srv, env, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			want := "Warning: " + changePasswordArgsWarning + "\n"
			if structured {
				want = `{"warning":{"code":"password_on_command_line","message":"` + changePasswordArgsWarning + `"}}` + "\n"
			}
			if stderr != want {
				t.Errorf("stderr = %q, want %q", stderr, want)
			}
			if strings.Contains(stderr, "new-pw") || strings.Contains(stdout, "warning") {
				t.Errorf("stdout=%q stderr=%q, want the warning on stderr only, without the password", stdout, stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

func TestChangePassword_PasswordArgumentWarningComesBeforeThePrompt(t *testing.T) {
	srv := changePasswordServer(t)
	var stdout, stderr strings.Builder
	var stderrAtPrompt []string
	prompt := func(label string) (string, error) {
		stderrAtPrompt = append(stderrAtPrompt, stderr.String())
		return "typed-new", nil
	}
	root := t.TempDir()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	code := RunWithLoginPrompts(&stdout, &stderr,
		[]string{"change-password", "--current-password", "pw", "--confirm", "fixture-user@example.invalid", "--llm"},
		srv.Client(), srv.URL+"/ajax", filepath.Join(root, "config.toml"), filepath.Join(root, "session.json"),
		func(key string) string { return env[key] }, LoginPrompts{Secret: prompt})
	if code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if len(stderrAtPrompt) != 2 {
		t.Fatalf("prompted %d times, want the new password twice", len(stderrAtPrompt))
	}
	if !strings.HasPrefix(stderrAtPrompt[0], `{"warning":{"code":"password_on_command_line"`) {
		t.Errorf("stderr before the first prompt = %q, want the structured warning", stderrAtPrompt[0])
	}
}

func TestAuthCommands_SelectIsCheckedBeforeDispatch(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	tests := map[string]struct {
		args []string
		want string
	}{
		"login into scalar":           {args: []string{"login", "--select", "username.x"}, want: "single value"},
		"logout into scalar":          {args: []string{"logout", "--select", "status.x"}, want: "single value"},
		"change-password into scalar": {args: []string{"change-password", "--confirm", "fixture-user@example.invalid", "--new-password", "n", "--select", "status.x"}, want: "single value"},
		"login dry run":               {args: []string{"login", "--dry-run", "--select", "id"}, want: "dry-run fields: status, username"},
		"logout dry run":              {args: []string{"logout", "--dry-run", "--select", "id"}, want: "dry-run fields: status"},
		"change-password dry run":     {args: []string{"change-password", "--confirm", "fixture-user@example.invalid", "--dry-run", "--select", "id"}, want: "dry-run fields: status"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, env, tc.args)
			if code != 4 || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want invalid_argument containing %q", code, stdout, stderr, tc.want)
			}
			if n := len(srv.Requests()); n != 0 {
				t.Errorf("sent %d requests, want none before the --select check", n)
			}
		})
	}
}
