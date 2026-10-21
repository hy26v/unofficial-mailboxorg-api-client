// ABOUTME: CLI integration tests for the share commands: create/delete infostore share
// ABOUTME: links, human and LLM output, validation, dry-run, and destructive safeguards.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestShareCreate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data": map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "entity": 9001, "is_new": true},
	}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "create", "--folder", "36", "--id", "4", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"url":"https://share.mailbox.org/ajax/share/tok/x","entity":9001,"is_new":true}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var gotMethod string
	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/share/management" && req.URL.Query().Get("action") == "getLink" {
			gotMethod = req.Method
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if gotMethod != http.MethodPut {
		t.Errorf("create method = %q, want PUT", gotMethod)
	}
	wantBody := map[string]any{"module": "infostore", "folder": "36", "item": "36/4"}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("create request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestShareCreate_RejectsMissingEntity(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data": map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "is_new": true},
	}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "create", "--folder", "36", "--id", "4", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing share entity rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestShareCreate_RejectsNonPositivePermissionEntity(t *testing.T) {
	for _, entity := range []int{-1, 0} {
		t.Run(strconv.Itoa(entity), func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
				"data": map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "entity": entity, "is_new": true},
			}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"share", "create", "--folder", "36", "--id", "4", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid permission entity rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestShareCreate_RejectsMissingNewFlag(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data": map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "entity": 4},
	}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "create", "--folder", "36", "--id", "4", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing is_new flag rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestShareCreate_RejectsInsecureLinkURL(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data": map[string]any{"url": "http://share.mailbox.org/ajax/share/tok/x", "entity": 4, "is_new": true},
	}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "create", "--folder", "36", "--id", "4", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want insecure share URL rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestShareCreate_MissingRequiredFlags_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"share", "create", "--id", "4", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--folder") {
		t.Errorf("stderr = %q, want mention of missing --folder", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls, got %d requests", len(srv.Requests()))
	}
}

func TestShare_RejectsUnsupportedModulesLocally(t *testing.T) {
	tests := map[string][]string{
		"create": {"share", "create", "--folder", "36", "--id", "4", "--module", "calendar", "--dry-run", "--llm"},
		"delete": {"share", "delete", "--folder", "36", "--id", "4", "--module", "calendar", "--confirm", "4", "--dry-run", "--llm"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code == 0 {
				t.Fatalf("exit code = 0, want unsupported module rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "--module") {
				t.Fatalf("stderr = %q, want module validation error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("unsupported module made %d requests, want local rejection", len(srv.Requests()))
			}
		})
	}
}

func TestShareCreate_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "create", "--folder", "36", "--id", "4", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/share/management" {
			t.Fatalf("dry-run sent a request to /ajax/share/management: %s", req.URL)
		}
	}
}

// share create has always accepted --confirm and ignored it, although its registry entry
// does not list the flag. This test keeps that behavior until the flag set is decided.
func TestShareCreate_AcceptsAndIgnoresConfirm(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"share", "create", "--folder", "36", "--id", "4", "--confirm", "9", "--dry-run", "--llm",
	})
	if code != 0 || stdout != `{"dry_run":true,"folder":"36","item":"36/4"}`+"\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q, want the dry run with --confirm ignored", code, stdout, stderr)
	}
}

func TestShareDelete_MissingConfirm_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "delete", "--folder", "36", "--id", "4", "--llm",
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
		t.Errorf("expected no network calls, got %d requests", len(srv.Requests()))
	}
}

func TestShareDelete_ConfirmMismatch_RefusesWithoutNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "delete", "--folder", "36", "--id", "4", "--confirm", "5", "--llm",
	})

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

func TestShareDelete_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "delete", "--folder", "36", "--id", "4", "--confirm", "4", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "dry_run") {
		t.Errorf("stdout = %q, want dry_run indicator", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/share/management" {
			t.Fatalf("dry-run sent a request to /ajax/share/management: %s", req.URL)
		}
	}
}

func TestShareDelete_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	handleShareLinkCheck(srv, "36/4", "36", true)
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data":      map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "entity": 9001, "is_new": false},
		"timestamp": 1700000000000,
	}})
	srv.Handle("/ajax/share/management", "deleteLink", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "delete", "--folder", "36", "--id", "4", "--confirm", "4", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	want := `{"deleted":true,"folder":"36","id":"4"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var foundGet bool
	var gotMethod string
	var gotTimestamp string
	var gotBody map[string]any
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/share/management" && req.URL.Query().Get("action") == "getLink" {
			foundGet = true
			if req.Method != http.MethodPut {
				t.Errorf("getLink method = %q, want PUT", req.Method)
			}
			var body map[string]any
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decoding getLink request body: %v", err)
			}
			wantBody := map[string]any{"module": "infostore", "folder": "36", "item": "36/4"}
			if !reflect.DeepEqual(body, wantBody) {
				t.Errorf("getLink request body = %#v, want %#v", body, wantBody)
			}
		}
		if req.URL.Path == "/ajax/share/management" && req.URL.Query().Get("action") == "deleteLink" {
			gotMethod = req.Method
			gotTimestamp = req.URL.Query().Get("timestamp")
			body, _ := io.ReadAll(req.Body)
			json.Unmarshal(body, &gotBody)
		}
	}
	if !foundGet {
		t.Error("no getLink preflight request observed")
	}
	if gotMethod != http.MethodPut {
		t.Errorf("deleteLink method = %q, want PUT", gotMethod)
	}
	if gotTimestamp != "1700000000000" {
		t.Errorf("timestamp query param = %q, want %q (from prior getLink)", gotTimestamp, "1700000000000")
	}
	wantBody := map[string]any{"module": "infostore", "folder": "36", "item": "36/4"}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("deleteLink request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestShareDelete_RejectsInvalidPreflightEntity(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	handleShareLinkCheck(srv, "36/4", "36", true)
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data":      map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "entity": 0, "is_new": false},
		"timestamp": 1700000000000,
	}})
	srv.Handle("/ajax/share/management", "deleteLink", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "delete", "--folder", "36", "--id", "4", "--confirm", "4", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want invalid preflight entity rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/share/management" && req.URL.Query().Get("action") == "deleteLink" {
			t.Fatal("deleteLink request sent after getLink returned an invalid entity")
		}
	}
}

func TestShare_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"share", "bogus"})

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

func TestDescribe_LLM_ShareCreate(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "share.create", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"share.create"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
}

func TestCommands_LLM_ListsShare(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "share", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"share.create"`) || !strings.Contains(stdout, `"name":"share.delete"`) {
		t.Errorf("stdout = %q, want share.create and share.delete listed", stdout)
	}
}

// handleShareLinkCheck answers the non-creating infostore list read that share delete
// uses to find a public (anonymous) link before it calls getLink.
func handleShareLinkCheck(srv *testutil.Server, item, folder string, hasLink bool) {
	permissions := []any{map[string]any{"type": "user", "entity": 7}}
	if hasLink {
		permissions = append(permissions, map[string]any{"type": "anonymous", "share_url": "https://share.mailbox.org/ajax/share/tok/x"})
	}
	srv.Handle("/ajax/infostore", "list", testutil.Response{Body: map[string]any{
		"data": []any{[]any{item, folder, permissions}},
	}})
}

func shareActions(srv *testutil.Server) []string {
	var actions []string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/share/management" || req.URL.Path == "/ajax/infostore" {
			actions = append(actions, req.URL.Path+"?"+req.URL.Query().Get("action"))
		}
	}
	return actions
}

func TestShareDelete_NoExistingLink_DoesNotCallGetLink(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	handleShareLinkCheck(srv, "36/4", "36", false)
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data":      map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "entity": 9001, "is_new": true},
		"timestamp": 1700000000000,
	}})
	srv.Handle("/ajax/share/management", "deleteLink", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"share", "delete", "--folder", "36", "--id", "4", "--confirm", "4", "--llm"})

	if code != 3 {
		t.Fatalf("exit code = %d, want 3 (not_found); stdout=%s stderr=%s", code, stdout, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, `"code":"not_found"`) {
		t.Errorf("stdout=%q stderr=%q, want not_found error only", stdout, stderr)
	}
	if got, want := shareActions(srv), []string{"/ajax/infostore?list"}; !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want only the non-creating check %v", got, want)
	}
	var check *http.Request
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/infostore" {
			check = req
		}
	}
	if check.Method != http.MethodPut || check.URL.Query().Get("columns") != "1,20,7010" {
		t.Errorf("check = %s %s, want PUT with columns 1,20,7010", check.Method, check.URL)
	}
	var body []map[string]string
	if err := json.NewDecoder(check.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, []map[string]string{{"id": "36/4", "folder": "36"}}) {
		t.Errorf("check body = %v (err %v), want the one requested item", body, err)
	}
}

func TestShareDelete_GetLinkCreatedNewLink_DeletesItAndReports(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	handleShareLinkCheck(srv, "36/4", "36", true)
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data":      map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/new", "entity": 9001, "is_new": true},
		"timestamp": 1700000000001,
	}})
	srv.Handle("/ajax/share/management", "deleteLink", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"share", "delete", "--folder", "36", "--id", "4", "--confirm", "4", "--llm"})

	if code != 3 {
		t.Fatalf("exit code = %d, want 3; stdout=%s stderr=%s", code, stdout, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "created a new link") || !strings.Contains(stderr, "no public link remains") {
		t.Errorf("stdout=%q stderr=%q, want a clear race report", stdout, stderr)
	}
	want := []string{"/ajax/infostore?list", "/ajax/share/management?getLink", "/ajax/share/management?deleteLink"}
	if got := shareActions(srv); !reflect.DeepEqual(got, want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "deleteLink" && req.URL.Query().Get("timestamp") != "1700000000001" {
			t.Errorf("deleteLink timestamp = %q, want the new link's timestamp", req.URL.Query().Get("timestamp"))
		}
	}
}

func TestShareDelete_NewLinkCleanupFails_NamesTheLink(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	handleShareLinkCheck(srv, "36/4", "36", true)
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data":      map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/new", "entity": 9001, "is_new": true},
		"timestamp": 1700000000001,
	}})
	srv.Handle("/ajax/share/management", "deleteLink", testutil.Response{Body: map[string]any{
		"error": "conflict", "code": "SHR-0020", "categories": "CONFLICT",
	}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"share", "delete", "--folder", "36", "--id", "36/4", "--confirm", "36/4", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	for _, want := range []string{"share_link_left_behind", "NEW public share link", "https://share.mailbox.org/ajax/share/tok/new", "item=36/4", "folder=36", "SHR-0020"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
}

func TestShare_AcceptsCompositeIDFromFilesList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
		"data": map[string]any{"url": "https://share.mailbox.org/ajax/share/tok/x", "entity": 9001, "is_new": false},
	}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"share", "create", "--folder", "36", "--id", "36/2", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") != "getLink" {
			continue
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["item"] != "36/2" {
			t.Errorf("getLink item = %v, want 36/2 (not 36/36/2)", body["item"])
		}
	}
}

func TestShare_RejectsCompositeIDFromAnotherFolder(t *testing.T) {
	for _, args := range [][]string{
		{"share", "create", "--folder", "37", "--id", "36/2", "--llm"},
		{"share", "delete", "--folder", "37", "--id", "36/2", "--confirm", "36/2", "--llm"},
	} {
		srv := testutil.NewServer()
		stdout, stderr, code := run(t, srv, map[string]string{}, args)
		if code != 4 || stdout != "" || !strings.Contains(stderr, "does not belong to --folder 37") {
			t.Errorf("%v: code=%d stdout=%q stderr=%q, want invalid_argument", args[1], code, stdout, stderr)
		}
		if len(srv.Requests()) != 0 {
			t.Errorf("%v: sent %d requests, want none", args[1], len(srv.Requests()))
		}
		srv.Close()
	}
}
