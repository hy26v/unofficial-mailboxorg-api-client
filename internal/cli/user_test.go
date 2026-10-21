// ABOUTME: CLI integration tests for the user update command: dry-run preview, successful
// ABOUTME: update round trip, validation, API errors, and stdout/stderr/exit-code separation.
package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestUserUpdate_MissingFields_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"user", "update", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--display-name") {
		t.Errorf("stderr = %q, want mention of --display-name/--timezone", stderr)
	}
}

func TestUserUpdate_RejectsInvalidTimezoneBeforeAuthentication(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"user", "update", "--timezone", "Mars/Olympus_Mons", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "timezone") {
		t.Fatalf("stderr = %q, want timezone validation error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid timezone made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestUserUpdate_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"user", "update", "--bogus"})

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

func TestUserUpdate_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 42, "email1": "fixture-user@example.invalid", "display_name": "Old Name",
		}, "timestamp": 111},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"user", "update", "--display-name", "New Name", "--dry-run"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Old Name") || !strings.Contains(stdout, "New Name") {
		t.Errorf("stdout = %q, want before/after values", stdout)
	}

	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/user" && req.URL.Query().Get("action") == "update" {
			t.Fatalf("dry-run must not send the update request, got %v", req.URL)
		}
	}
}

func TestUserUpdate_RejectsProfileWithoutID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{
			"data":      map[string]any{"display_name": "Old Name", "timezone": "Europe/Madrid"},
			"timestamp": 111,
		},
	})
	srv.Handle("/ajax/user", "update", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"user", "update", "--display-name", "New Name", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/user" && req.URL.Query().Get("action") == "update" {
			t.Fatalf("sent update with missing profile id: %s", req.URL)
		}
	}
}

func TestUserUpdate_Success_SendsIDAndTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 42, "email1": "fixture-user@example.invalid", "display_name": "Old Name",
		}, "timestamp": 111},
	})
	srv.Handle("/ajax/user", "update", testutil.Response{Body: map[string]any{"data": 222}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"user", "update", "--display-name", "New Name"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "New Name") {
		t.Errorf("stdout = %q, want New Name", stdout)
	}

	found := false
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/user" && req.URL.Query().Get("action") == "update" {
			found = true
			if req.Method != http.MethodPut {
				t.Errorf("update request method = %q, want PUT", req.Method)
			}
			if req.URL.Query().Get("id") != "42" {
				t.Errorf("id query param = %q, want 42", req.URL.Query().Get("id"))
			}
			if req.URL.Query().Get("timestamp") != "111" {
				t.Errorf("timestamp query param = %q, want 111", req.URL.Query().Get("timestamp"))
			}
			var updateBody map[string]string
			if err := json.NewDecoder(req.Body).Decode(&updateBody); err != nil {
				t.Fatalf("decode user update request body: %v", err)
			}
			if len(updateBody) != 1 || updateBody["display_name"] != "New Name" {
				t.Errorf("user update body display_name/field count = %q/%d, want %q/1",
					updateBody["display_name"], len(updateBody), "New Name")
			}
		}
	}
	if !found {
		t.Fatal("expected an update request to be sent")
	}
}

func TestUserUpdate_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 42, "email1": "fixture-user@example.invalid", "display_name": "Old Name",
		}, "timestamp": 111},
	})
	srv.Handle("/ajax/user", "update", testutil.Response{Body: map[string]any{"data": 222}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"user", "update", "--display-name", "New Name", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if !strings.Contains(stdout, `"display_name":"New Name"`) {
		t.Errorf("stdout = %q, want updated display_name field", stdout)
	}
}

func TestUserUpdate_APIError_OnUpdate(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 42, "email1": "fixture-user@example.invalid", "display_name": "Old Name",
		}, "timestamp": 111},
	})
	srv.Handle("/ajax/user", "update", testutil.Response{
		Body: map[string]any{"error": "conflict", "code": "USR-0001", "categories": "CONFLICT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"user", "update", "--display-name", "New Name", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "USR-0001") {
		t.Errorf("stderr = %q, want USR-0001 code", stderr)
	}
}

func TestUserUpdate_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"user", "update", "--display-name", "New Name", "--llm"})

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

func TestUserUpdate_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"user", "update", "--display-name", "New Name"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestUserUpdate_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"user", "bogus"})

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

func TestUserUpdate_DryRunHumanTextSaysNoChangeSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{Body: map[string]any{
		"data": map[string]any{"id": 42, "display_name": "Old", "timezone": "Europe/Berlin"}, "timestamp": 1,
	}})
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	stdout, stderr, code := run(t, srv, env, []string{"user", "update", "--display-name", "New", "--dry-run"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "no request sent") || !strings.Contains(stdout, "no change sent") {
		t.Errorf("stdout = %q, want \"no change sent\" after the sign-in and read", stdout)
	}
}
