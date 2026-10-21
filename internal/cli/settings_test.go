// ABOUTME: CLI integration tests for the settings get/set commands: jslob tree reads, single
// ABOUTME: key extraction, dry-run preview, single-key writes, and stdout/stderr/exit-code separation.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestSettingsGet_MissingPath_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"settings", "get", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--path") {
		t.Errorf("stderr = %q, want mention of --path", stderr)
	}
}

func TestSettingsGet_HumanMode_FullTree(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread", "count": 5}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "get", "--path", "io.ox/mail"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"sort"`) || !strings.Contains(stdout, "thread") {
		t.Errorf("stdout = %q, want tree contents", stdout)
	}

	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/jslob" && req.URL.Query().Get("action") == "list" {
			if req.Method != "PUT" {
				t.Errorf("method = %q, want PUT", req.Method)
			}
		}
	}
}

func TestSettingsGet_WithKey_SingleValue(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread", "count": 5}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "get", "--path", "io.ox/mail", "--key", "sort", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"value":"thread"`) {
		t.Errorf("stdout = %q, want value=thread", stdout)
	}
}

func TestSettingsGet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread"}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "get", "--path", "io.ox/mail", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
}

func TestSettingsGet_RejectsNullTree(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": nil},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"settings", "get", "--path", "io.ox/mail", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSettingsGet_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": nil},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"settings", "get", "--path", "io.ox/mail", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want null collection rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestSettingsGet_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"error": "nope", "code": "SVL-0021", "categories": "ERROR"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "get", "--path", "io.ox/mail", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "SVL-0021") {
		t.Errorf("stderr = %q, want SVL-0021 code", stderr)
	}
}

func TestSettingsSet_MissingFields_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"settings", "set", "--path", "io.ox/mail"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--key") {
		t.Errorf("stderr = %q, want mention of --key/--value", stderr)
	}
}

func TestSettingsSet_NestedKeyRejected(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"settings", "set", "--path", "io.ox/mail", "--key", "a.b", "--value", "1"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "top-level") {
		t.Errorf("stderr = %q, want mention of top-level key restriction", stderr)
	}
}

func TestSettingsSet_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread"}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--dry-run"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "thread") || !strings.Contains(stdout, "name") {
		t.Errorf("stdout = %q, want before/after values", stdout)
	}

	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/jslob" && req.URL.Query().Get("action") == "update" {
			t.Fatalf("dry-run must not send the set request, got %v", req.URL)
		}
	}
}

func TestSettingsSet_DryRun_PreservesLargeInteger(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sequence": 1}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "sequence",
		"--value", "9007199254740993", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"new_value":9007199254740993`) {
		t.Errorf("stdout = %q, want exact integer preserved", stdout)
	}
}

func TestSettingsSet_Success_SendsSingleKeyBody(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: []byte(`{"data":[{"id":"io.ox/mail","tree":{"sequence":9007199254740993}}]}`),
	})
	srv.Handle("/ajax/jslob", "update", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "sequence",
		"--value", "9007199254740993", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != "{\"path\":\"io.ox/mail\",\"key\":\"sequence\",\"old_value\":9007199254740993,\"new_value\":9007199254740993}\n" {
		t.Errorf("stdout = %q, want exact structured result", stdout)
	}

	var setRequest *http.Request
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/jslob" && req.URL.Query().Get("action") == "update" {
			if setRequest != nil {
				t.Fatal("more than one jslob set request was sent")
			}
			setRequest = req
		}
	}
	if setRequest == nil {
		t.Fatal("expected a set request to be sent")
	}
	if setRequest.Method != http.MethodPut {
		t.Errorf("method = %q, want PUT", setRequest.Method)
	}
	if setRequest.URL.Query().Get("id") != "io.ox/mail" {
		t.Errorf("id query param = %q, want io.ox/mail", setRequest.URL.Query().Get("id"))
	}
	body, err := io.ReadAll(setRequest.Body)
	if err != nil {
		t.Fatalf("could not read set request body: %v", err)
	}
	if string(body) != `{"sequence":9007199254740993}` {
		t.Errorf("request body = %q, want exactly one top-level key", body)
	}
}

func TestSettingsSet_RejectsUnappliedWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread"}},
		}},
	})
	srv.Handle("/ajax/jslob", "update", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want unapplied write rejected; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
	var listRequests int
	for _, request := range srv.Requests() {
		if request.URL.Path == "/ajax/jslob" && request.URL.Query().Get("action") == "list" {
			listRequests++
		}
	}
	if listRequests != 2 {
		t.Errorf("jslob list requests = %d, want pre-write and post-write reads", listRequests)
	}
}

func TestSettingsSet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "name"}},
		}},
	})
	srv.Handle("/ajax/jslob", "update", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if !strings.Contains(stdout, `"new_value":"name"`) {
		t.Errorf("stdout = %q, want new_value field", stdout)
	}
}

func TestSettingsSet_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread"}},
		}},
	})
	srv.Handle("/ajax/jslob", "update", testutil.Response{
		Body: map[string]any{"error": "nope", "code": "SVL-0011", "categories": "ERROR"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "SVL-0011") {
		t.Errorf("stderr = %q, want SVL-0011 code", stderr)
	}
}

func TestSettingsSet_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestSettings_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"settings", "bogus"})

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

func TestSettings_NoAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"settings"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should describe missing action")
	}
}

// TestSettingsGet_MismatchedJSLobID_DoesNotReturnAnotherTree pins the guarantee that a
// jslob read never substitutes a different settings tree for the requested one. The
// deployment answers PUT /jslob?action=list with the entries it actually matched, so an
// answer that carries only some other id means the requested path was not read.
func TestSettingsGet_MismatchedJSLobID_DoesNotReturnAnotherTree(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/core", "tree": map[string]any{"theme": "dark"}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "get", "--path", "io.ox/mail", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%s", stdout)
	}
	if strings.Contains(stdout, "dark") {
		t.Errorf("stdout = %q, want no contents from the io.ox/core tree", stdout)
	}
	if !strings.Contains(stderr, "io.ox/mail") {
		t.Errorf("stderr = %q, want mention of the requested path", stderr)
	}
}

// TestSettingsSet_MismatchedJSLobID_DoesNotWrite pins that a settings write is abandoned
// when the pre-read did not return the requested tree, rather than reporting an old_value
// taken from an unrelated tree.
func TestSettingsSet_MismatchedJSLobID_DoesNotWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/core", "tree": map[string]any{"sort": "date"}},
		}},
	})
	srv.Handle("/ajax/jslob", "update", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", "thread", "--llm"})

	if code == 0 {
		t.Fatal("exit code = 0, want nonzero")
	}
	if !strings.Contains(stderr, "io.ox/mail") {
		t.Errorf("stderr = %q, want mention of the requested path", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/jslob" && req.URL.Query().Get("action") == "update" {
			t.Fatal("a jslob set request was sent despite the pre-read not returning the requested tree")
		}
	}
}

func TestSettingsSet_DuplicateJSLobID_DoesNotWrite(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "date"}},
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread"}},
		}},
	})
	srv.Handle("/ajax/jslob", "update", testutil.Response{Body: map[string]any{}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", "sender", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/jslob" && req.URL.Query().Get("action") == "update" {
			t.Fatal("a jslob set request was sent after an ambiguous pre-read")
		}
	}
}

// fakeJSLobServer keeps one stored jslob tree and applies the documented semantics:
// action=set stores the body as the whole tree, action=update merges the body into it
// (deeply, so old subkeys of an object stay), and action=update with {"path","value"}
// puts the value at that key.
// dropOnUpdate simulates a server that loses other keys on update.
type fakeJSLobServer struct {
	mu           sync.Mutex
	tree         map[string]any
	actions      []string
	dropOnUpdate bool
}

func (f *fakeJSLobServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path + "?" + action {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
			return
		}
		f.actions = append(f.actions, action)
		switch r.URL.Path + "?" + action {
		case "/ajax/jslob?list":
			writeJSONResponse(w, map[string]any{"data": []any{map[string]any{"id": "io.ox/mail", "tree": f.tree}}})
		case "/ajax/jslob?set", "/ajax/jslob?update":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode jslob body: %v", err)
			}
			if path, ok := body["path"].(string); ok && action == "update" {
				f.tree[path] = body["value"]
				writeJSONResponse(w, map[string]any{"data": map[string]any{}})
				return
			}
			if action == "set" || f.dropOnUpdate {
				f.tree = map[string]any{}
			}
			deepMergeJSLob(f.tree, body)
			writeJSONResponse(w, map[string]any{"data": map[string]any{}})
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestSettingsSet_MergesAndKeepsOtherKeys(t *testing.T) {
	fake := &fakeJSLobServer{tree: map[string]any{"sort": "thread", "count": float64(5), "layout": map[string]any{"pane": "right"}}}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"new_value":"name"`) {
		t.Errorf("stdout = %q, want new_value name", stdout)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, action := range fake.actions {
		if action == "set" {
			t.Fatal("settings set sent jslob action=set, which replaces the whole tree")
		}
	}
	want := map[string]any{"sort": "name", "count": float64(5), "layout": map[string]any{"pane": "right"}}
	if !reflect.DeepEqual(fake.tree, want) {
		t.Errorf("stored tree = %#v, want other keys kept: %#v", fake.tree, want)
	}
}

func deepMergeJSLob(into, from map[string]any) {
	for key, value := range from {
		next, isObject := value.(map[string]any)
		current, wasObject := into[key].(map[string]any)
		if isObject && wasObject {
			deepMergeJSLob(current, next)
			continue
		}
		into[key] = value
	}
}

func TestSettingsSet_ObjectValueReplacesOldSubkeys(t *testing.T) {
	fake := &fakeJSLobServer{tree: map[string]any{"sort": "thread", "layout": map[string]any{"pane": "right", "size": float64(3)}}}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "layout", "--value", `{"pane":"left"}`, "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"new_value":{"pane":"left"}`) {
		t.Errorf("stdout = %q, want the new object value", stdout)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	want := map[string]any{"sort": "thread", "layout": map[string]any{"pane": "left"}}
	if !reflect.DeepEqual(fake.tree, want) {
		t.Errorf("stored tree = %#v, want the object replaced and other keys kept: %#v", fake.tree, want)
	}
}

func TestSettingsSet_RejectsObjectValueForSlashKeyWithoutWrite(t *testing.T) {
	fake := &fakeJSLobServer{tree: map[string]any{"sort": "thread"}}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	_, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "a/b", "--value", `{"x":1}`, "--llm",
	})
	if code != 4 || !strings.Contains(stderr, "invalid_argument") {
		t.Fatalf("exit code = %d, stderr = %q; want invalid_argument", code, stderr)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, action := range fake.actions {
		if action == "update" || action == "set" {
			t.Fatalf("sent jslob action=%s for a rejected value", action)
		}
	}
}

func TestSettingsSet_ReportsOtherKeysLostByWrite(t *testing.T) {
	fake := &fakeJSLobServer{tree: map[string]any{"sort": "thread", "count": 5}, dropOnUpdate: true}
	srv := httptest.NewServer(fake.handler(t))
	defer srv.Close()

	stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{
		"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure when another key was lost; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "count") || !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response that names the lost key count", stderr)
	}
}

func TestSettingsSet_DryRunHumanTextSaysNoChangeSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "thread"}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", `"name"`, "--dry-run"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "no request sent") {
		t.Errorf("stdout = %q, must not claim no request was sent after sign-in and a read", stdout)
	}
	if !strings.Contains(stdout, "no change sent") {
		t.Errorf("stdout = %q, want \"no change sent\"", stdout)
	}
}

func TestSettings_HumanModeEscapesTerminalControls(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/jslob", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "io.ox/mail", "tree": map[string]any{"sort": "a\u009b2Jb\u202ec"}},
		}},
	})
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, args := range [][]string{
		{"settings", "get", "--path", "io.ox/mail"},
		{"settings", "set", "--path", "io.ox/mail", "--key", "sort", "--value", "x", "--dry-run"},
	} {
		stdout, stderr, code := run(t, srv, env, args)
		if code != 0 {
			t.Fatalf("%v: exit code = %d; stderr=%s", args[1], code, stderr)
		}
		if strings.ContainsAny(stdout, "\u009b\u202e") || !strings.Contains(stdout, `\u009b2Jb\u202ec`) {
			t.Errorf("%v: stdout = %q, want C1 and bidi controls escaped", args[1], stdout)
		}
	}
}

func TestSettingsHelp_ShowsTheSettingsCommands(t *testing.T) {
	for _, args := range [][]string{{"settings", "--help"}, {"help", "settings"}, {"settings", "-h"}} {
		srv := testutil.NewServer()
		stdout, stderr, code := run(t, srv, map[string]string{}, args)
		srv.Close()
		if code != 0 {
			t.Fatalf("%v: exit code = %d; stderr=%s", args, code, stderr)
		}
		if !strings.Contains(stdout, "settings.get") || !strings.Contains(stdout, "settings.set") || strings.Contains(stdout, "config.toml") {
			t.Errorf("%v: stdout = %q, want the settings command list, not the config file help", args, stdout)
		}
	}
	for _, topic := range []string{"config", "configuration"} {
		srv := testutil.NewServer()
		stdout, _, code := run(t, srv, map[string]string{}, []string{"help", topic})
		srv.Close()
		if code != 0 || !strings.Contains(stdout, "config.toml") {
			t.Errorf("help %s: code=%d stdout=%q, want the config file help", topic, code, stdout)
		}
	}
}
