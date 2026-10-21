// ABOUTME: CLI integration tests for the mailfilter list/create/update/delete commands:
// ABOUTME: human and LLM output, validation, dry-run, delete confirmation, and API errors.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func requestBody(t *testing.T, r *http.Request) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("could not read request body: %v", err)
	}
	return string(b)
}

func decodedMailfilterRequestBody(t *testing.T, r *http.Request) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(requestBody(t, r)))
	decoder.UseNumber()
	var body map[string]any
	if err := decoder.Decode(&body); err != nil {
		t.Fatalf("could not decode request body: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("request body contained more than one JSON value: %v", err)
	}
	return body
}

func mailfilterTestServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	return srv
}

func TestMailfilterList_HumanMode_Success(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "rulename": "Archive newsletters", "active": true, "actioncmds": []any{map[string]any{"id": "keep"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Archive newsletters") {
		t.Errorf("stdout = %q, want rulename present", stdout)
	}
}

func TestMailfilterList_LLMMode_CompactJSON(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "rulename": "Rule A", "active": true, "actioncmds": []any{map[string]any{"id": "keep"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	var decoded struct {
		Rules []mailfilterRule `json:"rules"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("could not decode stdout as JSON: %v (%q)", err, stdout)
	}
	if len(decoded.Rules) != 1 || decoded.Rules[0].RuleName != "Rule A" {
		t.Errorf("decoded = %+v, want one rule named Rule A", decoded)
	}
}

func TestMailfilterList_RejectsNullCollection(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailfilterList_RejectsMissingRuleID(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"rulename": "Rule A", "active": true, "actioncmds": []any{map[string]any{"id": "keep"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing rule id rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailfilterList_RejectsMissingRuleName(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "active": true, "actioncmds": []any{map[string]any{"id": "keep"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing rule name rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailfilterList_RejectsMissingActiveFlag(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "rulename": "Rule A", "actioncmds": []any{map[string]any{"id": "keep"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing active flag rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailfilterList_RejectsMissingActionCommands(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "rulename": "Rule A", "active": true},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing action commands rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailfilterCreate_Success_SendsJSONBody(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "new", testutil.Response{Body: map[string]any{"data": 7}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	rule := `{"rulename":"New rule","active":false,"test":{"id":"true"},"actioncmds":[{"id":"keep"}],"sequence":9007199254740993}`
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "create", "--rule", rule, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != "{\"id\":7}\n" {
		t.Errorf("stdout = %q, want exact create result", stdout)
	}

	var newReq *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/mailfilter" && r.URL.Query().Get("action") == "new" {
			newReq = r
		}
	}
	if newReq == nil {
		t.Fatal("no request to mailfilter?action=new")
	}
	if newReq.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", newReq.Method)
	}
	wantBody := map[string]any{
		"rulename":   "New rule",
		"active":     false,
		"test":       map[string]any{"id": "true"},
		"actioncmds": []any{map[string]any{"id": "keep"}},
		"sequence":   json.Number("9007199254740993"),
	}
	if got := decodedMailfilterRequestBody(t, newReq); !reflect.DeepEqual(got, wantBody) {
		t.Errorf("request body = %#v, want %#v", got, wantBody)
	}
}

func TestMailfilterCreate_MissingRulename_InvalidArgumentError(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "create", "--rule", `{"actioncmds":[{"id":"keep"}]}`})

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "rulename") {
		t.Errorf("stderr = %q, want mention of rulename", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests sent, got %d", len(srv.Requests()))
	}
}

func TestMailfilterCreate_InvalidJSON_InvalidArgumentError(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"mailfilter", "create", "--rule", `not json`})

	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "invalid_argument") && !strings.Contains(stderr, "JSON") {
		t.Errorf("stderr = %q, want JSON validation error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests sent (auth not even attempted), got %d", len(srv.Requests()))
	}
}

func TestMailfilterCreate_DryRun_NoNetworkCall(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	rule := `{"rulename":"New rule","actioncmds":[{"id":"keep"}]}`
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "create", "--rule", rule, "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests sent for --dry-run, got %d", len(srv.Requests()))
	}
}

func TestMailfilterCreate_DryRun_PreservesLargeInteger(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	rule := `{"rulename":"Exact number","actioncmds":[],"sequence":9007199254740993}`
	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"mailfilter", "create", "--rule", rule, "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"sequence":9007199254740993`) {
		t.Errorf("stdout = %q, want exact rule integer preserved", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry run made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestMailfilterCreate_RejectsNullActionCommands(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	rule := `{"rulename":"No actions","actioncmds":null}`
	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"mailfilter", "create", "--rule", rule, "--dry-run", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want null actioncmds rejected; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "actioncmds") || !strings.Contains(stderr, "array") {
		t.Errorf("stderr = %q, want actioncmds array validation", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid rule made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestMailfilterUpdate_Success_SendsIDAndBody(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "update", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	rule := `{"rulename":"Updated rule","active":false,"test":{"id":"true"},"actioncmds":[{"id":"keep"}],"sequence":9007199254740993}`
	stdout, stderr, code := run(t, srv, env, []string{
		"mailfilter", "update", "--id", "7", "--rule", rule, "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != "{\"updated\":true,\"id\":7}\n" {
		t.Errorf("stdout = %q, want exact update result", stdout)
	}

	var updReq *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/mailfilter" && r.URL.Query().Get("action") == "update" {
			updReq = r
		}
	}
	if updReq == nil {
		t.Fatal("no request to mailfilter?action=update")
	}
	if updReq.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", updReq.Method)
	}
	wantBody := map[string]any{
		"id":         json.Number("7"),
		"rulename":   "Updated rule",
		"active":     false,
		"test":       map[string]any{"id": "true"},
		"actioncmds": []any{map[string]any{"id": "keep"}},
		"sequence":   json.Number("9007199254740993"),
	}
	if got := decodedMailfilterRequestBody(t, updReq); !reflect.DeepEqual(got, wantBody) {
		t.Errorf("request body = %#v, want %#v", got, wantBody)
	}
}

func TestMailfilterUpdate_MissingID_InvalidArgumentError(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"mailfilter", "update", "--rule", `{"active":false}`})

	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "--id") {
		t.Errorf("stderr = %q, want mention of --id", stderr)
	}
}

func TestMailfilterUpdate_RejectsNullRule(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"mailfilter", "update", "--id", "7", "--rule", "null", "--dry-run", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "object") {
		t.Fatalf("stderr = %q, want rule-object error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid rule made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestMailfilterDelete_MissingConfirm_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"mailfilter", "delete", "--id", "7"})

	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want mention of --confirm", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests sent (safeguard is local), got %d", len(srv.Requests()))
	}
}

func TestMailfilterDelete_ConfirmMismatch_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"mailfilter", "delete", "--id", "7", "--confirm", "8"})

	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want mention of --confirm", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests sent, got %d", len(srv.Requests()))
	}
}

func TestMailfilterDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "delete", "--id", "7", "--confirm", "7", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run marker", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no requests sent for --dry-run, got %d", len(srv.Requests()))
	}
}

func TestMailfilterDelete_Success_SendsID(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "delete", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "delete", "--id", "7", "--confirm", "7", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != "{\"deleted\":true,\"id\":7}\n" {
		t.Errorf("stdout = %q, want exact delete result", stdout)
	}

	var delReq *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/mailfilter" && r.URL.Query().Get("action") == "delete" {
			delReq = r
		}
	}
	if delReq == nil {
		t.Fatal("no request to mailfilter?action=delete")
	}
	if delReq.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", delReq.Method)
	}
	wantBody := map[string]any{"id": json.Number("7")}
	if got := decodedMailfilterRequestBody(t, delReq); !reflect.DeepEqual(got, wantBody) {
		t.Errorf("request body = %#v, want %#v", got, wantBody)
	}
}

func TestMailfilterDelete_APIError(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "delete", testutil.Response{
		Body: map[string]any{"error": "not found", "code": "MAIL_FILTER-0001", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"mailfilter", "delete", "--id", "7", "--confirm", "7", "--llm"})

	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "MAIL_FILTER-0001") {
		t.Errorf("stderr = %q, want mapped error code", stderr)
	}
}

func TestMailfilterUnknownAction_InvalidArgumentError(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"mailfilter", "bogus"})

	if code == 0 {
		t.Fatal("exit code = 0, want non-zero")
	}
	if !strings.Contains(stderr, "unknown mailfilter action") {
		t.Errorf("stderr = %q, want unknown action message", stderr)
	}
}

func TestDescribe_LLM_MailfilterDelete(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, nil, []string{"describe", "mailfilter.delete", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"destructive"`) {
		t.Errorf("stdout = %q, want destructive effect", stdout)
	}
}

func TestCommands_LLM_ListsMailfilterOperations(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, nil, []string{"commands", "mailfilter", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	for _, name := range []string{"mailfilter.list", "mailfilter.get", "mailfilter.create", "mailfilter.update", "mailfilter.delete"} {
		if !strings.Contains(stdout, name) {
			t.Errorf("stdout missing %q", name)
		}
	}
}

func TestMailfilterGet_HumanMode_Success(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "rulename": "Rule A", "active": true, "actioncmds": []any{map[string]any{"id": "keep"}}},
			map[string]any{"id": 2, "rulename": "Archive newsletters", "active": true, "test": map[string]any{"id": "true"}, "actioncmds": []any{map[string]any{"id": "stop"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "get", "--id", "2"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Archive newsletters") {
		t.Errorf("stdout = %q, want rulename present", stdout)
	}
}

func TestMailfilterGet_LLMMode_CompactJSON(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "rulename": "Rule A", "active": true, "actioncmds": []any{map[string]any{"id": "keep"}}},
			map[string]any{"id": 2, "rulename": "Rule B", "active": false, "actioncmds": []any{map[string]any{"id": "stop"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "get", "--id", "2", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	var got mailfilterRule
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout not valid JSON: %v; stdout=%s", err, stdout)
	}
	if got.ID != 2 || got.RuleName != "Rule B" {
		t.Errorf("got = %+v, want id=2 rulename=Rule B", got)
	}
}

func TestMailfilterGet_OptionalFieldsRemainSelectable(t *testing.T) {
	tests := []struct {
		field string
		want  string
	}{
		{field: "flags", want: `{"flags":[]}` + "\n"},
		{field: "position", want: `{"position":0}` + "\n"},
		{field: "test", want: `{"test":null}` + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			srv := mailfilterTestServer()
			defer srv.Close()
			srv.Handle("/ajax/mailfilter", "list", testutil.Response{
				Body: map[string]any{"data": []any{
					map[string]any{
						"id": 2, "rulename": "Rule B", "active": false,
						"flags": []any{}, "position": 0, "actioncmds": []any{},
					},
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"mailfilter", "get", "--id", "2", "--format", "json", "--select", tt.field,
			})

			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if stdout != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
		})
	}
}

func TestMailfilterGet_RejectsNullCollection(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "get", "--id", "2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailfilterGet_NotFound(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "rulename": "Rule A", "active": true, "actioncmds": []any{map[string]any{"id": "keep"}}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "get", "--id", "99", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "not_found") {
		t.Errorf("stderr = %q, want not_found error code", stderr)
	}
}

func TestMailfilterGet_MissingID(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, nil, []string{"mailfilter", "get", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want non-zero; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_argument") {
		t.Errorf("stderr = %q, want invalid_argument error code", stderr)
	}
}

func TestMailfilterActions_RejectNegativeIDsLocally(t *testing.T) {
	tests := map[string][]string{
		"get":    {"mailfilter", "get", "--id", "-1", "--llm"},
		"update": {"mailfilter", "update", "--id", "-1", "--rule", `{"active":false}`, "--dry-run", "--llm"},
		"delete": {"mailfilter", "delete", "--id", "-1", "--confirm", "-1", "--dry-run", "--llm"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			srv := mailfilterTestServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code == 0 {
				t.Fatalf("exit code = 0, want negative id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_argument") {
				t.Fatalf("stderr = %q, want invalid_argument", stderr)
			}
			if !strings.Contains(stderr, "--id") {
				t.Fatalf("stderr = %q, want id validation error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("negative id made %d requests, want local rejection", len(srv.Requests()))
			}
		})
	}
}

func TestDescribe_LLM_MailfilterGet(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, nil, []string{"describe", "mailfilter.get", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want read effect", stdout)
	}
}

// TestMailfilterCreate_UndecodableResponse_IsAnError is the regression test for a created
// rule reported under the wrong identifier. The create response's decode error was discarded,
// so any payload that was not a bare number left the id at its zero value and the command
// exited 0 announcing rule 0 - an id the caller would then pass to get, update, or delete.
func TestMailfilterCreate_UndecodableResponse_IsAnError(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range []struct {
		name string
		data any
	}{
		{"the created rule as an object", map[string]any{"id": 7}},
		{"the id as a string", "7"},
		{"no id at all", nil},
		{"an array", []any{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/mailfilter", "new", testutil.Response{Body: map[string]any{"data": tc.data}})

			stdout, stderr, code := run(t, srv, env, []string{
				"mailfilter", "create", "--rule", `{"rulename":"r","actioncmds":[]}`, "--llm",
			})

			if code == 0 {
				t.Fatalf("exit code = 0; stdout=%q", stdout)
			}
			if strings.Contains(stdout, `"id":0`) {
				t.Errorf("stdout = %q, reports rule id 0 for a rule the server created under another id", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Errorf("stderr = %q, want an invalid_response error", stderr)
			}
		})
	}
}

// TestMailfilterCreate_ReportsTheIDTheServerReturned pins the accepted shape so the stricter
// decoding above does not reject a real create response.
func TestMailfilterCreate_ReportsTheIDTheServerReturned(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mailfilter", "new", testutil.Response{Body: map[string]any{"data": 42}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mailfilter", "create", "--rule", `{"rulename":"r","actioncmds":[]}`, "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":42}`+"\n" {
		t.Errorf("stdout = %q, want the created rule id", stdout)
	}
}

func TestMailfilterCreate_RejectsNegativeRuleID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mailfilter", "new", testutil.Response{Body: map[string]any{"data": -1}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"mailfilter", "create", "--rule", `{"rulename":"r","actioncmds":[]}`, "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want negative rule id rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestMailfilterGet_HumanModeEscapesTerminalControlsInRuleName(t *testing.T) {
	srv := mailfilterTestServer()
	defer srv.Close()
	srv.Handle("/ajax/mailfilter", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 2, "rulename": "Safe\u202eexe.pdf\u009b2J\nactive: false", "active": true, "actioncmds": []any{}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"mailfilter", "get", "--id", "2"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.ContainsAny(stdout, "\u202e\u009b") || !strings.Contains(stdout, `Safe\u202eexe.pdf\u009b2J\nactive: false`) {
		t.Errorf("stdout = %q, want the rule name escaped on its own line", stdout)
	}
}
