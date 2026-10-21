// ABOUTME: CLI integration tests for tasks list/get/create/update/delete: default-folder
// ABOUTME: discovery, human and LLM output, validation, dry-run, destructive safeguards, and error mapping.
package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestTasksList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"32", "calendar"},
			[]any{"35", "tasks"},
		}},
	})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{1, "Buy milk", 1700000000000, 1},
			[]any{2, "Call bank", nil, 3},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "Buy milk") || !strings.Contains(stdout, "Call bank") {
		t.Errorf("stdout = %q, want task titles present", stdout)
	}
}

func TestTasksList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"35", "tasks"},
		}},
	})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{1, "Buy milk", 1700000000000, 1},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	want := `{"items":[{"id":"1","title":"Buy milk","status":1,"end_date":1700000000000}],"truncated":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestTasksList_RejectsInvalidTaskID(t *testing.T) {
	tests := []struct {
		name string
		id   any
	}{
		{name: "zero", id: 0},
		{name: "negative", id: -1},
		{name: "fractional", id: 1.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/tasks", "all", testutil.Response{
				Body: map[string]any{"data": []any{[]any{tt.id, "Unusable task", nil, 1}}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"tasks", "list", "--folder", "35", "--llm",
			})

			if code == 0 {
				t.Fatalf("id %v: exit code = 0, want failure; stdout=%s", tt.id, stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("id %v: stderr = %q, want invalid_response", tt.id, stderr)
			}
		})
	}
}

func TestTasksList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "all", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--folder", "35", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksList_DiscoverFolder_ModuleFilterNotAppliedServerSide(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	// Confirmed live: on this deployment, folders?action=list ignores the module=tasks
	// query filter and returns every folder regardless of module, so discovery must
	// filter client-side using column 301 (module), not just take the first row.
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"32", "calendar"},
			[]any{"33", "contacts"},
			[]any{"35", "tasks"},
			[]any{"default0/Trash", "mail"},
		}},
	})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{1, "Buy milk", nil, 1},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Buy milk") {
		t.Errorf("stdout = %q, want task title present", stdout)
	}

	for _, r := range srv.Requests() {
		if strings.Contains(r.URL.Path, "tasks") && r.URL.Query().Get("action") == "all" {
			if got := r.URL.Query().Get("folder"); got != "35" {
				t.Errorf("folder param = %q, want 35 (the row whose module is \"tasks\"), not the first row", got)
			}
		}
	}
}

// TestTasksList_DefaultFolderIsTheConfiguredStandardFolder is the regression test for the
// default tasks folder: discovery picked the first private tasks folder, which need not be
// the standard one. GET /config/folder/tasks names the standard folder.
func TestTasksList_DefaultFolderIsTheConfiguredStandardFolder(t *testing.T) {
	for _, configured := range []any{"36", 36} {
		t.Run(fmt.Sprintf("%T", configured), func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/config/folder/tasks", "", testutil.Response{Body: map[string]any{"data": configured, "timestamp": 1}})
			srv.Handle("/ajax/folders", "list", testutil.Response{
				Body: map[string]any{"data": []any{[]any{"35", "tasks"}, []any{"36", "tasks"}}},
			})
			srv.Handle("/ajax/tasks", "all", testutil.Response{Body: map[string]any{"data": []any{}}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			_, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			var sawConfig bool
			for _, r := range srv.Requests() {
				if r.URL.Path == "/ajax/config/folder/tasks" {
					sawConfig = r.Method == http.MethodGet
				}
				if r.URL.Path == "/ajax/folders" {
					t.Errorf("folder list request %s, want none when the configured folder is known", r.URL)
				}
				if r.URL.Path == "/ajax/tasks" && r.URL.Query().Get("folder") != "36" {
					t.Errorf("tasks folder = %q, want the configured standard folder 36", r.URL.Query().Get("folder"))
				}
			}
			if !sawConfig {
				t.Error("no GET /config/folder/tasks request observed")
			}
		})
	}
}

func TestTasksList_RejectsMalformedDiscoveredFolder(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{[]any{35, "tasks"}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, request := range srv.Requests() {
		if request.URL.Path == "/ajax/tasks" {
			t.Fatalf("malformed folder response triggered tasks request: %s", request.URL)
		}
	}
}

func TestTasksList_RejectsExtraFolderDiscoveryColumns(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{[]any{"35", "tasks", "unexpected"}}},
	})
	srv.Handle("/ajax/tasks", "all", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure for a folder row wider than the requested schema; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, request := range srv.Requests() {
		if request.URL.Path == "/ajax/tasks" {
			t.Fatalf("malformed folder response triggered tasks request: %s", request.URL)
		}
	}
}

func TestTasksList_RejectsNullFolderDiscoveryCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, request := range srv.Requests() {
		if request.URL.Path == "/ajax/tasks" {
			t.Fatalf("null folder response triggered tasks request: %s", request.URL)
		}
	}
}

func TestTasksList_ExplicitFolder_SkipsDiscovery(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{"data": []any{}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--folder", "35", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"items":[],"truncated":false}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
	for _, r := range srv.Requests() {
		if strings.Contains(r.URL.Path, "folders") {
			t.Errorf("expected no folder-discovery request when --folder is given, got %s", r.URL.String())
		}
	}
}

func TestTasksList_BoundedLimit_Truncates(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	rows := make([]any, 0, 3)
	for i := 1; i <= 3; i++ {
		rows = append(rows, []any{i, "task", nil, 1})
	}
	srv.Handle("/ajax/tasks", "all", testutil.Response{Body: map[string]any{"data": rows}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--folder", "35", "--limit", "2", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"truncated":true`) {
		t.Errorf("stdout = %q, want truncated:true", stdout)
	}
	if strings.Count(stdout, `"id"`) != 2 {
		t.Errorf("stdout = %q, want exactly 2 items", stdout)
	}
}

func TestTasksList_NoDefaultFolder_Error(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	// folders?action=list returns every folder regardless of the module=tasks filter, so
	// an account with no tasks folder still returns rows, just none with module "tasks".
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{"32", "calendar"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})

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

func TestTasksList_InvalidLimit_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "list", "--limit", "notanumber", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--limit") {
		t.Errorf("stderr = %q, want mention of --limit", stderr)
	}
}

func TestTasksList_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "list", "--bogus"})

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

func TestTasksList_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "bogus"})

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

func TestTasksList_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "list", "--llm"})

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

func TestTasksList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--folder", "35", "--llm"})

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

func TestTasksList_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"tasks", "list"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_TasksList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "tasks.list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"tasks.list"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestCommands_LLM_ListsTasksList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "tasks", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"tasks.list"`) {
		t.Errorf("stdout = %q, want tasks.list listed", stdout)
	}
}

func TestTasksMissingOrUnknownActionAdvertisesScopedDiscovery(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing action", args: []string{"tasks"}},
		{name: "unknown action", args: []string{"tasks", "bogus", "--llm"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, map[string]string{}, tt.args)
			if code == 0 || stdout != "" {
				t.Fatalf("exit code = %d stdout=%q, want argument failure", code, stdout)
			}
			if !strings.Contains(stderr, `mbx commands tasks`) {
				t.Fatalf("stderr = %q, want scoped discovery guidance", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("requests = %d, want local failure", len(srv.Requests()))
			}
		})
	}
}

func TestTasksGet_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data": map[string]any{
				"id": 4, "folder_id": 35, "title": "Buy milk", "note": "2%",
				"status": 1, "priority": 2, "end_date": 1700000000000,
			},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "get", "--id", "4", "--folder", "35", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"4","folder_id":"35","title":"Buy milk","note":"2%","status":1,"priority":2,"end_date":1700000000000,"timestamp":1784855706453}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestTasksGet_LLMMode_AcceptsNullStatus(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data": map[string]any{
				"id": 4, "folder_id": 35, "title": "Unscheduled task", "status": nil,
			},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "get", "--id", "4", "--folder", "35", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"4","folder_id":"35","title":"Unscheduled task","note":"","status":null,"priority":0,"end_date":null,"timestamp":1784855706453}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestTasksGet_OptionalFieldsRemainSelectable(t *testing.T) {
	tests := []struct {
		field string
		want  string
	}{
		{field: "note", want: `{"note":""}` + "\n"},
		{field: "priority", want: `{"priority":0}` + "\n"},
		{field: "end_date", want: `{"end_date":null}` + "\n"},
	}

	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/tasks", "get", testutil.Response{
				Body: map[string]any{
					"data": map[string]any{
						"id": 4, "folder_id": 35, "title": "Buy milk", "status": 1,
					},
					"timestamp": 1784855706453,
				},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"tasks", "get", "--id", "4", "--folder", "35", "--format", "json", "--select", tt.field,
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

func TestTasksGet_RejectsMissingTitle(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data":      map[string]any{"id": 4, "folder_id": 35, "status": 1},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "get", "--id", "4", "--folder", "35", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want missing task title rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data": map[string]any{
				"id": 9, "folder_id": 35, "title": "Wrong task", "status": 1,
			},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "get", "--id", "4", "--folder", "35", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksGet_RejectsMismatchedResponseFolder(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data": map[string]any{
				"id": 4, "folder_id": 36, "title": "Task from another folder", "status": 1,
			},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "get", "--id", "4", "--folder", "35", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure for a task from another folder; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksGet_RejectsInvalidResponseStatus(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data": map[string]any{
				"id": 4, "folder_id": 35, "title": "Invalid state", "status": 6,
			},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "get", "--id", "4", "--folder", "35", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want invalid task status rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksGet_RejectsInvalidResponsePriority(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data": map[string]any{
				"id": 4, "folder_id": 35, "title": "Invalid priority", "status": 1, "priority": 4,
			},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "get", "--id", "4", "--folder", "35", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want invalid task priority rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksGet_RejectsNonPositiveEndDate(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data": map[string]any{
				"id": 4, "folder_id": 35, "title": "Invalid due date", "status": 1, "end_date": 0,
			},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "get", "--id", "4", "--folder", "35", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want non-positive end date rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksGet_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "get", testutil.Response{
		Body: map[string]any{
			"data":      map[string]any{"id": 4, "folder_id": 35, "title": "Buy milk", "status": 1},
			"timestamp": 1784855706453,
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "get", "--id", "4", "--folder", "35"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "Buy milk") {
		t.Errorf("stdout = %q, want task title present", stdout)
	}
}

func TestTasksGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "get", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--id") {
		t.Errorf("stderr = %q, want mention of --id", stderr)
	}
}

func TestTasksCreate_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 4}, "timestamp": 1784855706453},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "create", "--folder", "35", "--title", "Buy milk",
		"--note", "2%", "--status", "1", "--priority", "2",
		"--end-date", "2030-01-02T12:00:00Z", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"4","folder_id":"35","timestamp":1784855706453}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	reqs := srv.Requests()
	var createReq *http.Request
	for _, r := range reqs {
		if strings.Contains(r.URL.Path, "tasks") && r.URL.Query().Get("action") == "new" {
			createReq = r
		}
	}
	if createReq == nil {
		t.Fatal("no tasks?action=new request seen")
	}
	if createReq.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", createReq.Method)
	}
	var gotBody map[string]any
	if err := json.NewDecoder(createReq.Body).Decode(&gotBody); err != nil {
		t.Fatalf("decoding create request body: %v", err)
	}
	wantBody := map[string]any{
		"folder_id": "35", "title": "Buy milk", "note": "2%",
		"status": float64(1), "priority": float64(2), "end_date": float64(1893585600000),
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("create request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestTasksCreate_RejectsNonPositiveResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 0}, "timestamp": 1784855706453},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "create", "--folder", "35", "--title", "Buy milk", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestTasksCreate_MissingTitle_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "create", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--title") {
		t.Errorf("stderr = %q, want mention of --title", stderr)
	}
}

func TestTasksWrite_RejectsOutOfRangeEnums(t *testing.T) {
	tests := []struct {
		name  string
		flag  string
		value string
	}{
		{name: "status", flag: "--status", value: "0"},
		{name: "priority", flag: "--priority", value: "4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, []string{
				"tasks", "create", "--folder", "35", "--title", "Buy milk", tt.flag, tt.value, "--llm",
			})

			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, tt.flag) {
				t.Fatalf("stderr = %q, want %s range error", stderr, tt.flag)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("invalid %s made %d HTTP requests, want 0", tt.flag, len(srv.Requests()))
			}
		})
	}
}

func TestTasksCreate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "create", "--folder", "35", "--title", "Buy milk", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if requests := srv.Requests(); len(requests) != 0 {
		t.Fatalf("dry-run made %d HTTP requests, want 0; first request=%s", len(requests), requests[0].URL)
	}
}

func TestTasksUpdate_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{}, "timestamp": 1784855739221},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "update", "--id", "4", "--folder", "35", "--timestamp", "1784855706453",
		"--title", "Buy milk (updated)", "--note", "bring coupons", "--status", "2",
		"--priority", "3", "--end-date", "2030-01-02T12:00:00Z", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"4","updated":true,"timestamp":1784855739221}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	reqs := srv.Requests()
	var updateReq *http.Request
	for _, r := range reqs {
		if strings.Contains(r.URL.Path, "tasks") && r.URL.Query().Get("action") == "update" {
			updateReq = r
		}
	}
	if updateReq == nil {
		t.Fatal("no tasks?action=update request seen")
	}
	if updateReq.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", updateReq.Method)
	}
	if updateReq.URL.Query().Get("id") != "4" || updateReq.URL.Query().Get("folder") != "35" || updateReq.URL.Query().Get("timestamp") != "1784855706453" {
		t.Errorf("query params = %v, want id/folder/timestamp set", updateReq.URL.Query())
	}
	var gotBody map[string]any
	if err := json.NewDecoder(updateReq.Body).Decode(&gotBody); err != nil {
		t.Fatalf("decoding update request body: %v", err)
	}
	wantBody := map[string]any{
		"title": "Buy milk (updated)", "note": "bring coupons",
		"status": float64(2), "priority": float64(3), "end_date": float64(1893585600000),
	}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("update request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestTasksUpdate_MissingTimestamp_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "update", "--id", "4", "--title", "x", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--timestamp") {
		t.Errorf("stderr = %q, want mention of --timestamp", stderr)
	}
}

func TestTasksWrite_RejectsNonPositiveTimestamp(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "update",
			args: []string{"tasks", "update", "--id", "4", "--folder", "35", "--timestamp", "0", "--title", "x", "--dry-run", "--llm"},
		},
		{
			name: "delete",
			args: []string{"tasks", "delete", "--id", "4", "--folder", "35", "--timestamp", "-1", "--confirm", "4", "--dry-run", "--llm"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "positive") {
				t.Fatalf("stderr = %q, want positive timestamp error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("invalid timestamp made %d HTTP requests, want 0", len(srv.Requests()))
			}
		})
	}
}

func TestTasksWrite_RejectsNonPositiveEndDate(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"tasks", "create", "--title", "Old task", "--folder", "35",
		"--end-date", "1970-01-01T00:00:00Z", "--dry-run", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want non-positive end date rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_argument") || !strings.Contains(stderr, "--end-date") {
		t.Fatalf("stderr = %q, want invalid_argument mentioning --end-date", stderr)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestTasksUpdate_NoFieldsGiven_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"tasks", "update", "--id", "4", "--timestamp", "123", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if stderr == "" {
		t.Errorf("stderr should explain that at least one field is required")
	}
}

func TestTasksUpdate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "update", "--id", "4", "--folder", "35", "--timestamp", "123", "--title", "x", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if requests := srv.Requests(); len(requests) != 0 {
		t.Fatalf("dry-run made %d HTTP requests, want 0; first request=%s", len(requests), requests[0].URL)
	}
}

// Destructive safeguard tests come before the successful-delete test, per project
// convention: the safeguard must refuse locally without any network call.

func TestTasksDelete_MissingConfirm_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}, []string{
		"tasks", "delete", "--id", "4", "--timestamp", "123", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want mention of --confirm", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls without --confirm, got %d requests", len(srv.Requests()))
	}
}

func TestTasksDelete_ConfirmMismatch_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}, []string{
		"tasks", "delete", "--id", "4", "--timestamp", "123", "--confirm", "5", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want mention of --confirm mismatch", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls on --confirm mismatch, got %d requests", len(srv.Requests()))
	}
}

func TestTasksDelete_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "delete", testutil.Response{
		Body: map[string]any{"data": []any{}, "timestamp": 1784855739221},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "delete", "--id", "4", "--folder", "35", "--timestamp", "1784855739221", "--confirm", "4", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"4","deleted":true}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	reqs := srv.Requests()
	var deleteReq *http.Request
	for _, r := range reqs {
		if strings.Contains(r.URL.Path, "tasks") && r.URL.Query().Get("action") == "delete" {
			deleteReq = r
		}
	}
	if deleteReq == nil {
		t.Fatal("no tasks?action=delete request seen")
	}
	if deleteReq.Method != http.MethodPut {
		t.Errorf("method = %s, want PUT", deleteReq.Method)
	}
	if deleteReq.URL.Query().Get("timestamp") != "1784855739221" {
		t.Errorf("timestamp query param = %q, want 1784855739221", deleteReq.URL.Query().Get("timestamp"))
	}
	var gotBody []map[string]any
	if err := json.NewDecoder(deleteReq.Body).Decode(&gotBody); err != nil {
		t.Fatalf("decoding delete request body: %v", err)
	}
	wantBody := []map[string]any{{"id": "4", "folder": "35"}}
	if !reflect.DeepEqual(gotBody, wantBody) {
		t.Errorf("delete request body = %#v, want %#v", gotBody, wantBody)
	}
}

func TestTasksDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}, []string{
		"tasks", "delete", "--id", "4", "--timestamp", "123", "--confirm", "4", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls during dry-run, got %d requests", len(srv.Requests()))
	}
}

func TestTasksDelete_APIError_MapsToStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "delete", testutil.Response{
		Body: map[string]any{
			"error": "You do not have the appropriate permisson to see the task.", "code": "TSK-0046", "categories": "PERMISSION_DENIED",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"tasks", "delete", "--id", "4", "--folder", "35", "--timestamp", "123", "--confirm", "4", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "TSK-0046") {
		t.Errorf("stderr = %q, want TSK-0046 code", stderr)
	}
}

func TestDescribe_LLM_TasksDelete_ShowsDestructive(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "tasks.delete", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"effect":"destructive"`) {
		t.Errorf("stdout = %q, want effect:destructive", stdout)
	}
}

// TestTasksListColumns_RequestOneColumnPerDocumentedField pins the tasks.list column list
// against the operation's documented output. OX numbers columns per module: 200/202 come
// from the block shared by appointments and tasks (title, end_date), but a task's status is
// 300, in the task-specific block. 220 in the shared block is "participants", an array.
func TestTasksListColumns_RequestOneColumnPerDocumentedField(t *testing.T) {
	columns := strings.Split(tasksListColumns, ",")

	op, ok := operations.Default.Get("tasks.list")
	if !ok {
		t.Fatal("tasks.list is not registered")
	}
	if len(columns) != len(op.Output.Fields) {
		t.Errorf("tasksListColumns = %q (%d columns), want one per documented field %v (%d)",
			tasksListColumns, len(columns), op.Output.Fields, len(op.Output.Fields))
	}
	for _, column := range columns {
		if column == "220" {
			t.Errorf("tasksListColumns = %q requests column 220 (participants), not a task field", tasksListColumns)
		}
	}
	if columns[len(columns)-1] != "300" {
		t.Errorf("tasksListColumns = %q, want the status column 300 last, matching the decode order", tasksListColumns)
	}
}

// TestTasksList_RequestsStatusColumnNotParticipants is the regression test for a silently
// wrong task status: tasks.list requested OX column 220 (participants, an array) where it
// decodes status, so the array failed to decode into an int, the error was discarded, and
// every task was reported with status 0 - a value outside the documented 1-5 range.
func TestTasksList_RequestsStatusColumnNotParticipants(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
	})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{"data": []any{[]any{1, "Buy milk", 1700000000000, 3}}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	columns := ""
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/tasks" && r.URL.Query().Get("action") == "all" {
			columns = r.URL.Query().Get("columns")
		}
	}
	if columns != "1,200,202,300" {
		t.Errorf("tasks?action=all columns = %q, want \"1,200,202,300\" (id, title, end_date, status)", columns)
	}
}

// TestTasksList_ReportsTheStatusTheServerReturned ties the requested columns to the decoded
// output: the fourth requested column is the task status, so a task the server reports as
// done (3) must be rendered as 3 rather than the zero value of a failed decode.
func TestTasksList_ReportsTheStatusTheServerReturned(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{
		Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
	})
	srv.Handle("/ajax/tasks", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{7, "Ship it", 1700000000000, 3},
			[]any{8, "Later", nil, 5},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"tasks", "list", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"items":[{"id":"7","title":"Ship it","status":3,"end_date":1700000000000},` +
		`{"id":"8","title":"Later","status":5,"end_date":null}],"truncated":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestTasksDelete_ProtectionSurvivesAFlagTheOperationDoesNotDeclare is the regression test
// for a delete-protection bypass. tasks delete shares its flag parser with create and update,
// so it accepted --title even though tasks.delete does not declare it. The parser consumed the
// following --dry-run as --title's value while the protection scan, which skips values only
// for declared flags, read that same --dry-run as a dry-run request and stepped aside. The
// command then performed a real deletion under a policy documented as having no command-line
// override.
func TestTasksDelete_ProtectionSurvivesAFlagTheOperationDoesNotDeclare(t *testing.T) {
	for _, flag := range []string{"--title", "--note", "--status", "--priority", "--end-date"} {
		t.Run(flag, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/folders", "list", testutil.Response{
				Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
			})
			srv.Handle("/ajax/tasks", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := runWithConfig(t, srv, env, []string{
				"tasks", "delete", flag, "--dry-run",
				"--id", "5", "--timestamp", "1", "--confirm", "5", "--llm",
			}, protectionConfig("all"))

			if code == 0 {
				t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if stderr == "" {
				t.Error("stderr should report why the command was rejected")
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("protected tasks.delete reached the network: %d requests", len(srv.Requests()))
			}
		})
	}
}

// TestTasksFlags_RejectFlagsTheOperationDoesNotDeclare pins the tasks flag parsing to the
// registry: each subcommand accepts exactly the arguments its operation documents, plus the
// global --llm. A parser that accepts the union of every subcommand's flags makes the CLI's
// own discovery output wrong and, for tasks delete, desynchronizes the delete-protection
// argument scan from the parser.
func TestTasksFlags_RejectFlagsTheOperationDoesNotDeclare(t *testing.T) {
	for _, tc := range []struct{ operation, flag string }{
		{"tasks.delete", "--title"},
		{"tasks.delete", "--note"},
		{"tasks.delete", "--status"},
		{"tasks.delete", "--priority"},
		{"tasks.delete", "--end-date"},
		{"tasks.create", "--id"},
		{"tasks.create", "--confirm"},
		{"tasks.create", "--timestamp"},
		{"tasks.update", "--confirm"},
	} {
		t.Run(tc.operation+" "+tc.flag, func(t *testing.T) {
			_, err := parseTasksFlagsForTest(tc.operation, []string{tc.flag, "value"})
			if err == nil {
				t.Fatalf("%s accepted %s, which it does not declare", tc.operation, tc.flag)
			}
			if !strings.Contains(err.Message, tc.flag) {
				t.Errorf("error = %q, want the rejected flag named", err.Message)
			}
		})
	}
}

// TestTasksFlags_AcceptEveryDeclaredArgument is the other half of the contract: a flag the
// operation documents must never be rejected as unknown.
func TestTasksFlags_AcceptEveryDeclaredArgument(t *testing.T) {
	for _, operation := range []string{"tasks.create", "tasks.update", "tasks.delete"} {
		op, ok := operations.Default.Get(operation)
		if !ok {
			t.Fatalf("%s is not registered", operation)
		}
		for name, spec := range op.Args {
			args := []string{"--" + name}
			if spec.TakesValue() {
				args = append(args, "1")
			}
			if _, err := parseTasksFlagsForTest(operation, args); err != nil && strings.HasPrefix(err.Message, "unknown flag") {
				t.Errorf("%s declares --%s but its parser does not accept it: %s", operation, name, err.Message)
			}
		}
	}
}

// parseTasksFlagsForTest parses args the way the tasks write handlers do: create and update
// with the task field rules, delete without them.
func parseTasksFlagsForTest(operation string, args []string) (commandFlags, *cliErrors.Error) {
	if operation == "tasks.delete" {
		return parseCommandFlags(operation, args)
	}
	return parseCommandFlags(operation, args, taskFieldRules()...)
}

// TestTasksCreate_RejectsDeleteOnlyFlags is the other half of the same contract: create must
// not silently swallow arguments that only belong to update or delete.
func TestTasksCreate_RejectsDeleteOnlyFlags(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"tasks", "create", "--title", "x", "--confirm", "5", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want the rejected flag named", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("rejected command contacted the API: %d requests", len(srv.Requests()))
	}
}

func TestTasks_DryRunFieldsMatchTheDryRunResults(t *testing.T) {
	assertDryRunFieldsAreRegistered(t, "tasks.create",
		[]string{"tasks", "create", "--title", "t"})
	assertDryRunFieldsAreRegistered(t, "tasks.update",
		[]string{"tasks", "update", "--id", "5", "--timestamp", "1", "--title", "t"})
	assertDryRunFieldsAreRegistered(t, "tasks.delete",
		[]string{"tasks", "delete", "--id", "5", "--timestamp", "1", "--confirm", "5"})
}

func TestTasks_SelectIsCheckedBeforeTheCommandRuns(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	stdout, stderr, code := run(t, srv, env, []string{"tasks", "get", "--id", "5", "--select", "title.text"})
	if code != 4 || stdout != "" || !strings.Contains(stderr, `holds a single value`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want a scalar-field error", code, stdout, stderr)
	}

	stdout, stderr, code = run(t, srv, env, []string{"tasks", "create", "--title", "t", "--dry-run", "--select", "id"})
	if code != 4 || stdout != "" || !strings.Contains(stderr, "dry-run fields: dry_run, would_create") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want a dry-run field error", code, stdout, stderr)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("rejected --select sent %d requests", n)
	}
}
