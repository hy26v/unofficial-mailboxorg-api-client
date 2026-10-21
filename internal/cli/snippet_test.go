// ABOUTME: CLI integration tests for the snippet commands (list/get/create/update/delete):
// ABOUTME: human and LLM output, validation, mutation safeguards, and API/auth failures.
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func decodeSnippetRequestBody(t *testing.T, req *http.Request) map[string]string {
	t.Helper()

	var body map[string]string
	decoder := json.NewDecoder(req.Body)
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		t.Fatalf("request body has trailing data: %v", err)
	}
	return body
}

func TestSnippetList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"snippets":[{"id":"2","displayname":"cli-test-snippet","content":"hello","module":"io.ox/mail","type":"signature","shared":false,"createdby":3}]}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestSnippetList_RejectsItemWithoutID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"displayname": "unusable snippet", "content": "hello", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_RejectsItemWithoutDisplayName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "content": "hello", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing display name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_RejectsItemWithoutContent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing content rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_RejectsItemWithoutModule(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"type": "signature", "shared": false, "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing module rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_RejectsItemWithoutType(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"module": "io.ox/mail", "shared": false, "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing type rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_RejectsItemWithoutSharedFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"module": "io.ox/mail", "type": "signature", "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing shared flag rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_RejectsItemWithoutCreator(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"module": "io.ox/mail", "type": "signature", "shared": false,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing creator rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "cli-test-snippet") {
		t.Errorf("stdout = %q, want displayname present", stdout)
	}
}

func TestSnippetList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "all", testutil.Response{
		Body: map[string]any{"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "list", "--llm"})

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

func TestSnippetGet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":"2","displayname":"cli-test-snippet","content":"hello","module":"io.ox/mail","type":"signature","shared":false,"createdby":3}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
}

func TestSnippetGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "3", "displayname": "wrong snippet", "content": "hello", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetGet_RejectsMissingDisplayName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "content": "hello", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing display name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetGet_RejectsMissingContent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing content rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetGet_RejectsMissingModule(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing module rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetGet_RejectsMissingType(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"module": "io.ox/mail", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing type rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetGet_RejectsMissingSharedFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"module": "io.ox/mail", "type": "signature", "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing shared flag rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetGet_RejectsMissingCreator(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello",
			"module": "io.ox/mail", "type": "signature", "shared": false,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing creator rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"snippet", "get", "--llm"})

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

func TestSnippetGet_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "get", testutil.Response{
		Body: map[string]any{"error": "not found", "code": "SNP-0003", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "get", "--id", "999", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "SNP-0003") {
		t.Errorf("stderr = %q, want SNP-0003 code", stderr)
	}
}

func TestSnippetCreate_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "new", testutil.Response{Body: map[string]any{"data": "2"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "create", "--displayname", "cli-test-snippet", "--content", "hello", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"id":"2"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}

	req := srv.Requests()[len(srv.Requests())-1]
	if req.Method != http.MethodPut {
		t.Errorf("method = %s, want %s", req.Method, http.MethodPut)
	}
	if req.URL.Path != "/ajax/snippet" {
		t.Errorf("path = %q, want /ajax/snippet", req.URL.Path)
	}
	if req.URL.Query().Get("action") != "new" {
		t.Errorf("action = %q, want new", req.URL.Query().Get("action"))
	}
	wantBody := map[string]string{
		"displayname": "cli-test-snippet",
		"content":     "hello",
		"module":      "io.ox/mail",
		"type":        "signature",
	}
	if got := decodeSnippetRequestBody(t, req); !reflect.DeepEqual(got, wantBody) {
		t.Errorf("body = %#v, want %#v", got, wantBody)
	}
}

func TestSnippetCreate_DefaultsTypeAndModule(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "new", testutil.Response{Body: map[string]any{"data": "2"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{
		"snippet", "create", "--displayname", "cli-test-snippet", "--content", "hello", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	req := srv.Requests()[len(srv.Requests())-1]
	body := req.Body
	buf := make([]byte, 4096)
	n, _ := body.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, `"type":"signature"`) {
		t.Errorf("body = %q, want default type signature", got)
	}
	if !strings.Contains(got, `"module":"io.ox/mail"`) {
		t.Errorf("body = %q, want default module io.ox/mail", got)
	}
}

func TestSnippetCreate_MissingDisplayName_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"snippet", "create", "--content", "hello", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--displayname") {
		t.Errorf("stderr = %q, want mention of --displayname", stderr)
	}
}

func TestSnippetCreate_MissingContent_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"snippet", "create", "--displayname", "x", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--content") {
		t.Errorf("stderr = %q, want mention of --content", stderr)
	}
}

func TestSnippetCreate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"}, []string{
		"snippet", "create", "--displayname", "cli-test-snippet", "--content", "hello", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
}

func TestSnippetUpdate_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet-updated", "content": "hello updated", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--displayname", "cli-test-snippet-updated", "--content", "hello updated", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"id":"2","displayname":"cli-test-snippet-updated","content":"hello updated","module":"io.ox/mail","type":"signature","shared":false,"createdby":3}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}

	req := srv.Requests()[len(srv.Requests())-1]
	if req.Method != http.MethodPut {
		t.Errorf("method = %s, want %s", req.Method, http.MethodPut)
	}
	if req.URL.Path != "/ajax/snippet" {
		t.Errorf("path = %q, want /ajax/snippet", req.URL.Path)
	}
	if req.URL.Query().Get("action") != "update" {
		t.Errorf("action = %q, want update", req.URL.Query().Get("action"))
	}
	if req.URL.Query().Get("id") != "2" {
		t.Errorf("id query param = %q, want 2", req.URL.Query().Get("id"))
	}
	wantBody := map[string]string{
		"displayname": "cli-test-snippet-updated",
		"content":     "hello updated",
	}
	if got := decodeSnippetRequestBody(t, req); !reflect.DeepEqual(got, wantBody) {
		t.Errorf("body = %#v, want %#v", got, wantBody)
	}
}

func TestSnippetUpdate_RejectsUnappliedRequestedFields(t *testing.T) {
	tests := []struct {
		name string
		args []string
		data map[string]any
	}{
		{
			name: "display name",
			args: []string{"--displayname", "cli-test-snippet-updated"},
			data: map[string]any{
				"id": "2", "displayname": "cli-test-snippet", "content": "hello", "module": "io.ox/mail",
				"type": "signature", "shared": false, "createdby": 3,
			},
		},
		{
			name: "content",
			args: []string{"--content", "hello updated"},
			data: map[string]any{
				"id": "2", "displayname": "cli-test-snippet", "content": "hello", "module": "io.ox/mail",
				"type": "signature", "shared": false, "createdby": 3,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/snippet", "update", testutil.Response{Body: map[string]any{"data": tc.data}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			args := append([]string{"snippet", "update", "--id", "2"}, tc.args...)
			args = append(args, "--llm")
			stdout, stderr, code := run(t, srv, env, args)

			if code == 0 {
				t.Fatalf("exit code = 0, want unapplied %s rejected; stdout=%s", tc.name, stdout)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty on failure", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestSnippetUpdate_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "3", "displayname": "Wrong snippet", "content": "hello updated", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--content", "hello updated", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetUpdate_RejectsMissingDisplayName(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "content": "hello updated", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--content", "hello updated", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing display name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetUpdate_RejectsMissingContent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "module": "io.ox/mail",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--displayname", "cli-test-snippet", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing content rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetUpdate_RejectsMissingModule(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello updated",
			"type": "signature", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--content", "hello updated", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing module rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetUpdate_RejectsMissingType(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello updated",
			"module": "io.ox/mail", "shared": false, "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--content", "hello updated", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing type rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetUpdate_RejectsMissingSharedFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello updated",
			"module": "io.ox/mail", "type": "signature", "createdby": 3,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--content", "hello updated", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing shared flag rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetUpdate_RejectsMissingCreator(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "2", "displayname": "cli-test-snippet", "content": "hello updated",
			"module": "io.ox/mail", "type": "signature", "shared": false,
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"snippet", "update", "--id", "2", "--content", "hello updated", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing creator rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSnippetUpdate_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"snippet", "update", "--content", "x", "--llm"})

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

func TestSnippetUpdate_NoFieldsToUpdate_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"snippet", "update", "--id", "2", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--content") && !strings.Contains(stderr, "--displayname") {
		t.Errorf("stderr = %q, want mention of at least one updatable field", stderr)
	}
}

func TestSnippetUpdate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"}, []string{
		"snippet", "update", "--id", "2", "--content", "hello updated", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
}

// Destructive-operation safeguard tests come before the success-path test, per the
// project's mutation-safety rules: the safeguard must be proven to refuse locally,
// without a network call, before the happy path is exercised.

func TestSnippetDelete_MissingConfirm_RefusesLocally_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"}, []string{
		"snippet", "delete", "--id", "2", "--llm",
	})

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
		t.Errorf("missing confirm made %d requests, want 0", len(srv.Requests()))
	}
}

func TestSnippetDelete_ConfirmMismatch_RefusesLocally_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"}, []string{
		"snippet", "delete", "--id", "2", "--confirm", "3", "--llm",
	})

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
		t.Errorf("mismatched confirm made %d requests, want 0", len(srv.Requests()))
	}
}

func TestSnippetDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u", "MBX_PASSWORD": "p"}, []string{
		"snippet", "delete", "--id", "2", "--confirm", "2", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("dry-run made %d requests, want 0", len(srv.Requests()))
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
}

func TestSnippetDelete_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "delete", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "delete", "--id", "2", "--confirm", "2", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"id":"2","deleted":true}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}

	req := srv.Requests()[len(srv.Requests())-1]
	if req.Method != http.MethodGet {
		t.Errorf("method = %s, want %s", req.Method, http.MethodGet)
	}
	if req.URL.Path != "/ajax/snippet" {
		t.Errorf("path = %q, want /ajax/snippet", req.URL.Path)
	}
	if req.URL.Query().Get("action") != "delete" {
		t.Errorf("action = %q, want delete", req.URL.Query().Get("action"))
	}
	if req.URL.Query().Get("id") != "2" {
		t.Errorf("id query param = %q, want 2", req.URL.Query().Get("id"))
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("body = %q, want empty", body)
	}
}

func TestSnippetDelete_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/snippet", "delete", testutil.Response{
		Body: map[string]any{"error": "not found", "code": "SNP-0003", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"snippet", "delete", "--id", "2", "--confirm", "2", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "SNP-0003") {
		t.Errorf("stderr = %q, want SNP-0003 code", stderr)
	}
}

func TestSnippetDelete_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"snippet", "delete", "--id", "2", "--confirm", "2"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_SnippetDelete(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "snippet.delete", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"effect":"destructive"`) {
		t.Errorf("stdout = %q, want effect destructive", stdout)
	}
}

func TestCommands_LLM_ListsAllSnippetOperations(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "snippet", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	for _, name := range []string{"snippet.list", "snippet.get", "snippet.create", "snippet.update", "snippet.delete"} {
		if !strings.Contains(stdout, `"name":"`+name+`"`) {
			t.Errorf("stdout = %q, want %s listed", stdout, name)
		}
	}
}

// TestSnippetUpdate_DryRunReportsOnlyTheFieldsItWouldSend is the regression test for a dry
// run that misreported the request it stands in for. snippet update builds its body from the
// flags actually given, but the dry run rendered a separate map that always carried both
// --content and --displayname, so updating only the display name reported that it would send
// an empty content, i.e. that it would blank the signature's body. A dry run whose whole
// purpose is to show what would be sent must not describe a mutation the real call would not
// make.
func TestSnippetUpdate_DryRunReportsOnlyTheFieldsItWouldSend(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{
			name:    "display name only",
			args:    []string{"snippet", "update", "--id", "2", "--displayname", "New name", "--dry-run", "--llm"},
			want:    []string{`"displayname":"New name"`, `"id":"2"`},
			notWant: []string{`"content"`},
		},
		{
			name:    "content only",
			args:    []string{"snippet", "update", "--id", "2", "--content", "Regards", "--dry-run", "--llm"},
			want:    []string{`"content":"Regards"`, `"id":"2"`},
			notWant: []string{`"displayname"`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := run(t, srv, env, tc.args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			for _, want := range tc.want {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout = %q, want %s", stdout, want)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(stdout, notWant) {
					t.Errorf("stdout = %q, reports %s, which the real request would not send", stdout, notWant)
				}
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("dry run made %d requests", len(srv.Requests()))
			}
		})
	}
}

// TestRenderDryRun_HumanFieldOrderIsStable pins the human dry-run rendering to a fixed field
// order. It walked a Go map, whose iteration order is randomized, so the same command printed
// its fields in a different order from one run to the next.
func TestRenderDryRun_HumanFieldOrderIsStable(t *testing.T) {
	fields := map[string]string{
		"displayname": "Sig",
		"content":     "Regards",
		"module":      "io.ox/mail",
		"type":        "signature",
	}

	var first string
	for i := 0; i < 50; i++ {
		var out bytes.Buffer
		renderDryRun(&out, false, "snippet.create", fields)
		if i == 0 {
			first = out.String()
			continue
		}
		if out.String() != first {
			t.Fatalf("run %d rendered a different order:\n%s\nwant:\n%s", i, out.String(), first)
		}
	}

	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	previous := -1
	for _, name := range names {
		at := strings.Index(first, "\n"+name)
		if at < 0 {
			t.Fatalf("rendered output is missing %q:\n%s", name, first)
		}
		if at < previous {
			t.Errorf("field %q is out of alphabetical order:\n%s", name, first)
		}
		previous = at
	}
}

// TestSnippetCreate_DryRunHumanOutputIsStable is the end-to-end half of the same guard.
func TestSnippetCreate_DryRunHumanOutputIsStable(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	var first string
	for i := 0; i < 50; i++ {
		stdout, stderr, code := run(t, srv, env, []string{
			"snippet", "create", "--displayname", "Sig", "--content", "Regards", "--dry-run",
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
		}
		if i == 0 {
			first = stdout
			continue
		}
		if stdout != first {
			t.Fatalf("run %d printed a different order:\n%s\nwant:\n%s", i, stdout, first)
		}
	}
}
