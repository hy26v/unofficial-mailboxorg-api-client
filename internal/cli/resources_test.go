// ABOUTME: CLI integration tests for the resources search command: human and LLM output,
// ABOUTME: validation, authentication failures, API failures, and stdout/stderr/exit-code separation.
package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestResourcesSearch_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 12, "display_name": "Conference Room A", "mailaddress": "room-a@mailbox.org"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Conference Room A") || !strings.Contains(stdout, "room-a@mailbox.org") {
		t.Errorf("stdout = %q, want resource fields present", stdout)
	}
}

func TestResourcesSearch_HumanMode_EscapesControlCharacters(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 12, "display_name": "Room\tA\nInjected", "mailaddress": "room-a@mailbox.org"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `Room\tA\nInjected`) {
		t.Errorf("stdout = %q, want control characters escaped inside one cell", stdout)
	}
	if got := strings.Count(stdout, "\n"); got != 2 {
		t.Errorf("stdout has %d lines, want one header and one resource row: %q", got, stdout)
	}
}

func TestResourcesSearch_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 12, "display_name": "Conference Room A", "mailaddress": "room-a@mailbox.org"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
		t.Errorf("stdout not JSON: %q", stdout)
	}
	want := `{"resources":[{"id":12,"display_name":"Conference Room A","email":"room-a@mailbox.org"}]}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestResourcesSearch_LLMMode_EmptyResult(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"resources":[]}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestResourcesSearch_RejectsNonPositiveResourceID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 0, "display_name": "Unusable", "mailaddress": "room@mailbox.org"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestResourcesSearch_RejectsMissingDisplayName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 12, "mailaddress": "room-a@mailbox.org"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing display name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestResourcesSearch_RejectsBlankDisplayName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 12, "display_name": "   ", "mailaddress": "room-a@mailbox.org"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want blank display name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestResourcesSearch_RejectsMissingMailAddress(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 12, "display_name": "Conference Room A"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing mail address rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestResourcesSearch_RejectsBlankMailAddress(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 12, "display_name": "Conference Room A", "mailaddress": "   "},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want blank mail address rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestResourcesSearch_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestResourcesSearch_CustomPattern_SentToServer(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{"data": []any{}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"resources", "search", "--pattern", "room*", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	reqs := srv.Requests()
	var found bool
	for _, r := range reqs {
		if r.URL.Path == "/ajax/resource" && r.URL.Query().Get("action") == "search" {
			found = true
			if r.Method != "PUT" {
				t.Errorf("method = %s, want PUT", r.Method)
			}
			if !strings.Contains(r.Header.Get("Content-Type"), "application/json") {
				t.Errorf("content-type = %q, want application/json", r.Header.Get("Content-Type"))
			}
		}
	}
	if !found {
		t.Fatal("no request to /ajax/resource?action=search observed")
	}
}

func TestResourcesSearch_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"resources", "search", "--bogus"})

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

func TestResourcesSearch_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"resources", "bogus"})

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

func TestResourcesSearch_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"resources", "search", "--llm"})

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

func TestResourcesSearch_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "search", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "search", "--llm"})

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

func TestResourcesSearch_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"resources", "search"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_ResourcesSearch(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "resources.search", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"resources.search"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
	assertGolden(t, "testdata/describe_resources_search_llm.golden", stdout)
}

func TestCommands_LLM_ListsResourcesSearch(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "resources", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"resources.search"`) {
		t.Errorf("stdout = %q, want resources.search listed", stdout)
	}
}

func TestResourcesCreate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 5}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "create",
		"--display-name", "Conference Room B", "--name", "room-b", "--mailaddress", "room-b@mailbox.org",
		"--description", "second floor", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":5}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "new" {
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotBody["display_name"] != "Conference Room B" || gotBody["name"] != "room-b" || gotBody["mailaddress"] != "room-b@mailbox.org" || gotBody["description"] != "second floor" {
		t.Errorf("request body = %v, want display_name/name/mailaddress/description", gotBody)
	}
}

func TestResourcesCreate_RejectsNonPositiveID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 0}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "create", "--display-name", "Conference Room B", "--name", "room-b", "--mailaddress", "room-b@mailbox.org", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestResourcesCreate_MissingRequiredFlags_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"resources", "create", "--name", "room-b", "--mailaddress", "room-b@mailbox.org", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--display-name") {
		t.Errorf("stderr = %q, want mention of missing --display-name", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls, got %d requests", len(srv.Requests()))
	}
}

func TestResourcesCreate_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "create",
		"--display-name", "Conference Room B", "--name", "room-b", "--mailaddress", "room-b@mailbox.org",
		"--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" {
			t.Fatalf("dry-run sent a request to /ajax/resource: %s", req.URL)
		}
	}
}

func TestResourcesUpdate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 5, "display_name": "Conference Room B", "name": "room-b", "mailaddress": "room-b@mailbox.org",
			"description": "second floor", "last_modified_utc": 1700000000000,
		}},
	})
	srv.Handle("/ajax/resource", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "update", "--id", "5", "--description", "third floor", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":5}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotTimestamp string
	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "update" {
			gotTimestamp = req.URL.Query().Get("timestamp")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotTimestamp != "1700000000000" {
		t.Errorf("timestamp query param = %q, want %q (from prior get)", gotTimestamp, "1700000000000")
	}
	if gotBody["description"] != "third floor" {
		t.Errorf("request body = %v, want description=\"third floor\"", gotBody)
	}
}

func TestResourcesUpdate_RejectsMismatchedLookupID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 6, "display_name": "Wrong Resource", "last_modified_utc": 1700000000000,
		}},
	})
	srv.Handle("/ajax/resource", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "update", "--id", "5", "--description", "third floor", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "update" {
			t.Fatalf("sent update after mismatched lookup: %s", req.URL)
		}
	}
}

func TestResourcesUpdate_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"resources", "update", "--description", "x", "--llm"})

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

func TestResourcesMutations_RejectNonPositiveIDsLocally(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "update_zero", args: []string{"resources", "update", "--id", "0", "--description", "Invalid", "--dry-run", "--llm"}},
		{name: "update_negative", args: []string{"resources", "update", "--id", "-1", "--description", "Invalid", "--dry-run", "--llm"}},
		{name: "delete_zero", args: []string{"resources", "delete", "--id", "0", "--confirm", "0", "--dry-run", "--llm"}},
		{name: "delete_negative", args: []string{"resources", "delete", "--id", "-1", "--confirm", "-1", "--dry-run", "--llm"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want non-positive --id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "--id") {
				t.Fatalf("stderr = %q, want mention of --id", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("non-positive --id sent %d network requests", len(srv.Requests()))
			}
		})
	}
}

func TestResourcesDelete_MissingConfirm_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "delete", "--id", "5", "--llm"})

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

func TestResourcesDelete_ConfirmMismatch_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"resources", "delete", "--id", "5", "--confirm", "6", "--llm"})

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

func TestResourcesDelete_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "delete", "--id", "5", "--confirm", "5", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" {
			t.Fatalf("dry-run sent a request to /ajax/resource: %s", req.URL)
		}
	}
}

func TestResourcesDelete_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 5, "display_name": "Conference Room B", "name": "room-b", "mailaddress": "room-b@mailbox.org",
			"last_modified_utc": 1700000000000,
		}},
	})
	srv.Handle("/ajax/resource", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "delete", "--id", "5", "--confirm", "5", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "\"deleted\":true") {
		t.Errorf("stdout = %q, want deleted indicator", stdout)
	}

	var gotTimestamp string
	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "delete" {
			gotTimestamp = req.URL.Query().Get("timestamp")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotTimestamp != "1700000000000" {
		t.Errorf("timestamp query param = %q, want %q (from prior get)", gotTimestamp, "1700000000000")
	}
	idFloat, _ := gotBody["id"].(float64)
	if int(idFloat) != 5 {
		t.Errorf("request body id = %v, want 5", gotBody["id"])
	}
}

func TestResourcesDelete_RejectsMismatchedLookupID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 6, "display_name": "Wrong Resource", "last_modified_utc": 1700000000000,
		}},
	})
	srv.Handle("/ajax/resource", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "delete", "--id", "5", "--confirm", "5", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "delete" {
			t.Fatalf("sent delete after mismatched lookup: %s", req.URL)
		}
	}
}

func TestResourcesUpdate_StringLastModifiedUTC_UsesFallbackTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 5, "display_name": "Conference Room B", "name": "room-b", "mailaddress": "room-b@mailbox.org",
			"last_modified_utc": "Fri Jul 24 03:52:19 CEST 2026",
		}},
	})
	srv.Handle("/ajax/resource", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "update", "--id", "5", "--description", "third floor", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":5}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotTimestamp string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "update" {
			gotTimestamp = req.URL.Query().Get("timestamp")
		}
	}
	if gotTimestamp == "" {
		t.Errorf("timestamp query param = %q, want a non-empty fallback timestamp", gotTimestamp)
	}
}

func TestResourcesUpdate_RejectsMissingLastModifiedUTC(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 5}},
	})
	srv.Handle("/ajax/resource", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "update", "--id", "5", "--description", "third floor", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing mutation timestamp rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "update" {
			t.Fatalf("sent update without a resource mutation timestamp: %s", req.URL)
		}
	}
}

func TestResourcesDelete_StringLastModifiedUTC_UsesFallbackTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 5, "display_name": "Conference Room B", "name": "room-b", "mailaddress": "room-b@mailbox.org",
			"last_modified_utc": "Fri Jul 24 03:52:19 CEST 2026",
		}},
	})
	srv.Handle("/ajax/resource", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "delete", "--id", "5", "--confirm", "5", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "\"deleted\":true") {
		t.Errorf("stdout = %q, want deleted indicator", stdout)
	}

	var gotTimestamp string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "delete" {
			gotTimestamp = req.URL.Query().Get("timestamp")
		}
	}
	if gotTimestamp == "" {
		t.Errorf("timestamp query param = %q, want a non-empty fallback timestamp", gotTimestamp)
	}
}

func TestResourcesDelete_RejectsMissingLastModifiedUTC(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/resource", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 5}},
	})
	srv.Handle("/ajax/resource", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"resources", "delete", "--id", "5", "--confirm", "5", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing mutation timestamp rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/resource" && req.URL.Query().Get("action") == "delete" {
			t.Fatalf("sent delete without a resource mutation timestamp: %s", req.URL)
		}
	}
}
