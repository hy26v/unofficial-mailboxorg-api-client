// ABOUTME: CLI integration tests for the group get command: human and LLM output,
// ABOUTME: validation, authentication failures, API failures, and stdout/stderr/exit-code separation.
package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestGroupGet_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{3},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "get", "--id", "0"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "All users") || !strings.Contains(stdout, "3") {
		t.Errorf("stdout = %q, want display_name and members present", stdout)
	}
}

func TestGroupGet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{3},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "get", "--id", "0", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if stdout != `{"id":0,"display_name":"All users","last_modified_utc":1784854680693,"members":[3]}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestGroupGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 6, "display_name": "Wrong Group", "last_modified_utc": 1784854680693,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "get", "--id", "5", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupGet_RejectsMissingZeroID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{3},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "get", "--id", "0", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing id rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupGet_RejectsMissingDisplayName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "last_modified_utc": 1784854680693, "members": []int{3},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "get", "--id", "0", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing display name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupGet_RejectsMissingMutationTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "display_name": "All users", "members": []int{3},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "get", "--id", "0", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing mutation timestamp rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupGet_RejectsMissingMembers(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "get", "--id", "0", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing members rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{3},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "All users") {
		t.Errorf("stdout = %q, want display_name present", stdout)
	}
}

func TestGroupList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{3},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if stdout != `{"groups":[{"id":0,"display_name":"All users","last_modified_utc":1784854680693,"members":[3]}]}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestGroupList_RejectsNegativeGroupID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": -1, "display_name": "Invalid", "last_modified_utc": 1784854680693, "members": []int{},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupList_RejectsMissingZeroID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{3},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing id rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupList_RejectsMissingMutationTimestamp(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": 0, "display_name": "All users", "members": []int{3},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing mutation timestamp rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupList_RejectsMissingDisplayName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": 0, "last_modified_utc": 1784854680693, "members": []int{3},
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing display name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupList_RejectsMissingMembers(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing members rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestGroupList_DefaultsToWildcardPattern(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"groups":[]}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestGroupList_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "list", "--bogus"})

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

func TestGroupList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "search", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "list", "--llm"})

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

func TestGroupList_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"group", "list"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_GroupList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "group.list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"group.list"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestCommands_LLM_ListsGroupList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "group", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"group.list"`) {
		t.Errorf("stdout = %q, want group.list listed", stdout)
	}
}

func TestGroupReads_RejectNonPositiveMemberIDs(t *testing.T) {
	tests := map[string]struct {
		action string
		body   any
		args   []string
	}{
		"get": {
			action: "get",
			body: map[string]any{"data": map[string]any{
				"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{0},
			}},
			args: []string{"group", "get", "--id", "0", "--llm"},
		},
		"list": {
			action: "search",
			body: map[string]any{"data": []any{map[string]any{
				"id": 0, "display_name": "All users", "last_modified_utc": 1784854680693, "members": []int{-1},
			}}},
			args: []string{"group", "list", "--llm"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/group", tc.action, testutil.Response{Body: tc.body})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, tc.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want non-positive member id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestGroupGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "get", "--llm"})

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

func TestGroupGet_InvalidID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "get", "--id", "abc", "--llm"})

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

func TestGroupActions_RejectNegativeIDsLocally(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "get", args: []string{"group", "get", "--id", "-1", "--llm"}},
		{name: "update", args: []string{"group", "update", "--id", "-1", "--display-name", "Invalid", "--dry-run", "--llm"}},
		{name: "delete", args: []string{"group", "delete", "--id", "-1", "--confirm", "-1", "--dry-run", "--llm"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want negative --id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "--id") {
				t.Fatalf("stderr = %q, want mention of --id", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("negative --id sent %d network requests", len(srv.Requests()))
			}
		})
	}
}

func TestGroupGet_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "get", "--bogus"})

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

func TestGroupGet_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "bogus"})

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

func TestGroupGet_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "get", "--id", "0", "--llm"})

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

func TestGroupGet_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "get", "--id", "0", "--llm"})

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

func TestGroupGet_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"group", "get", "--id", "0"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_GroupGet(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "group.get", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"group.get"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestCommands_LLM_ListsGroupGet(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "group", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"group.get"`) {
		t.Errorf("stdout = %q, want group.get listed", stdout)
	}
}

func TestGroupCreate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 4}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "create", "--display-name", "Friends", "--name", "friends", "--members", "3,7", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":4}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/group" && req.URL.Query().Get("action") == "new" {
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotBody["display_name"] != "Friends" || gotBody["name"] != "friends" {
		t.Errorf("request body = %v, want display_name=Friends name=friends", gotBody)
	}
	members, _ := gotBody["members"].([]any)
	if len(members) != 2 {
		t.Errorf("request body members = %v, want [3,7]", gotBody["members"])
	}
}

func TestGroupCreate_RejectsNonPositiveID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 0}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "create", "--display-name", "Friends", "--name", "friends", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestParseMemberIDs_RejectsNonPositiveIDs(t *testing.T) {
	for _, value := range []string{"0", "-1", "3,0", "3,-1"} {
		t.Run(value, func(t *testing.T) {
			if ids, err := parseMemberIDs(value); err == nil {
				t.Fatalf("parseMemberIDs(%q) = %v, nil; want invalid member id error", value, ids)
			}
		})
	}
}

func TestGroupCreate_MissingRequiredFlags_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "create", "--name", "friends", "--llm"})

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

func TestGroupCreate_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "create", "--display-name", "Friends", "--name", "friends", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/group" {
			t.Fatalf("dry-run sent a request to /ajax/group: %s", req.URL)
		}
	}
}

func TestGroupUpdate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 4, "display_name": "Friends", "name": "friends", "last_modified_utc": 1700000000000, "members": []int{3},
		}},
	})
	srv.Handle("/ajax/group", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "update", "--id", "4", "--display-name", "Close Friends", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":4}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotTimestamp string
	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/group" && req.URL.Query().Get("action") == "update" {
			gotTimestamp = req.URL.Query().Get("timestamp")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotTimestamp != "1700000000000" {
		t.Errorf("timestamp query param = %q, want %q (from prior get)", gotTimestamp, "1700000000000")
	}
	if gotBody["display_name"] != "Close Friends" {
		t.Errorf("request body = %v, want display_name=\"Close Friends\"", gotBody)
	}
}

func TestGroupUpdate_RejectsMismatchedLookupID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 5, "display_name": "Wrong Group", "last_modified_utc": 1700000000000,
		}},
	})
	srv.Handle("/ajax/group", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "update", "--id", "4", "--display-name", "Close Friends", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/group" && req.URL.Query().Get("action") == "update" {
			t.Fatalf("sent update after mismatched lookup: %s", req.URL)
		}
	}
}

func TestGroupMutations_RejectMissingLookupTimestamp(t *testing.T) {
	for _, tt := range []struct {
		name   string
		action string
		args   []string
		body   any
	}{
		{
			name: "update", action: "update",
			args: []string{"group", "update", "--id", "4", "--display-name", "Close Friends", "--llm"},
			body: map[string]any{"data": map[string]any{}},
		},
		{
			name: "delete", action: "delete",
			args: []string{"group", "delete", "--id", "4", "--confirm", "4", "--llm"},
			body: map[string]any{"data": []any{}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/group", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{
					"id": 4, "display_name": "Friends", "members": []int{3},
				}},
			})
			srv.Handle("/ajax/group", tt.action, testutil.Response{Body: tt.body})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want missing optimistic-lock timestamp rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
			for _, req := range srv.Requests() {
				if req.URL.Path == "/ajax/group" && req.URL.Query().Get("action") == tt.action {
					t.Fatalf("sent %s with missing lookup timestamp: %s", tt.action, req.URL)
				}
			}
		})
	}
}

func TestGroupUpdate_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"group", "update", "--display-name", "X", "--llm"})

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

func TestGroupDelete_MissingConfirm_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "delete", "--id", "4", "--llm"})

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

func TestGroupDelete_ConfirmMismatch_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"group", "delete", "--id", "4", "--confirm", "5", "--llm"})

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

func TestGroupDelete_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "delete", "--id", "4", "--confirm", "4", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/group" {
			t.Fatalf("dry-run sent a request to /ajax/group: %s", req.URL)
		}
	}
}

func TestGroupDelete_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 4, "display_name": "Friends", "name": "friends", "last_modified_utc": 1700000000000, "members": []int{3},
		}},
	})
	srv.Handle("/ajax/group", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "delete", "--id", "4", "--confirm", "4", "--llm",
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
		if req.URL.Path == "/ajax/group" && req.URL.Query().Get("action") == "delete" {
			gotTimestamp = req.URL.Query().Get("timestamp")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotTimestamp != "1700000000000" {
		t.Errorf("timestamp query param = %q, want %q (from prior get)", gotTimestamp, "1700000000000")
	}
	idFloat, _ := gotBody["id"].(float64)
	if int(idFloat) != 4 {
		t.Errorf("request body id = %v, want 4", gotBody["id"])
	}
}

func TestGroupDelete_RejectsMismatchedLookupID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 5, "display_name": "Wrong Group", "last_modified_utc": 1700000000000,
		}},
	})
	srv.Handle("/ajax/group", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"group", "delete", "--id", "4", "--confirm", "4", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/group" && req.URL.Query().Get("action") == "delete" {
			t.Fatalf("sent delete after mismatched lookup: %s", req.URL)
		}
	}
}

func TestGroupUpdate_DryRunShowsTheExactRequestBody(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/group", "get", testutil.Response{Body: map[string]any{
		"data": map[string]any{"id": 5, "last_modified_utc": 1700000000000},
	}})
	srv.Handle("/ajax/group", "update", testutil.Response{Body: map[string]any{"data": map[string]any{}}})
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	stdout, stderr, code := run(t, srv, env, []string{"group", "update", "--id", "5", "--members", "", "--dry-run", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if stdout != `{"dry_run":true,"id":5,"request":{"members":null}}`+"\n" {
		t.Errorf("dry-run stdout = %q, want the members:null body that the real run sends", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry run sent %d requests", len(srv.Requests()))
	}

	if _, stderr, code = run(t, srv, env, []string{"group", "update", "--id", "5", "--members", "", "--llm"}); code != 0 {
		t.Fatalf("real run exit code = %d; stderr=%s", code, stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") != "update" {
			continue
		}
		body, _ := io.ReadAll(req.Body)
		if string(body) != `{"members":null}` {
			t.Errorf("real update body = %s, want the dry-run request {\"members\":null}", body)
		}
	}
}
