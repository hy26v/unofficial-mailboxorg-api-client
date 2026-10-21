// ABOUTME: CLI integration tests for the reminder list and delete commands: bounded range
// ABOUTME: listing, human and LLM output, delete confirmation/dry-run safeguards, and errors.
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

func TestReminderList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "1", "module": 4, "target_id": "12", "folder": "35", "alarm": 1784844000000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "1") || !strings.Contains(stdout, "12") {
		t.Errorf("stdout = %q, want reminder fields present", stdout)
	}
}

func TestReminderList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "1", "module": 4, "target_id": "12", "folder": "35", "alarm": 1784844000000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	want := `{"items":[{"id":"1","module":4,"target_id":"12","folder":"35","alarm":1784844000000}],"truncated":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestReminderList_RejectsItemWithoutID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"module": 4, "target_id": "12", "folder": "35", "alarm": 1784844000000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestReminderList_RejectsInvalidIDs(t *testing.T) {
	for _, tt := range []struct {
		name string
		id   any
	}{
		{name: "zero", id: 0},
		{name: "negative", id: -1},
		{name: "fractional", id: 1.5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/reminder", "range", testutil.Response{
				Body: map[string]any{"data": []any{
					map[string]any{"id": tt.id, "module": 4, "target_id": "12", "folder": "35", "alarm": 1784844000000},
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
			})
			if code == 0 {
				t.Fatalf("id %v: exit code = 0, want invalid reminder id rejected; stdout=%s", tt.id, stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("id %v: stderr = %q, want invalid_response", tt.id, stderr)
			}
		})
	}
}

func TestReminderList_RejectsItemWithoutTargetID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "1", "module": 4, "folder": "35", "alarm": 1784844000000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing target_id rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestReminderList_RejectsInvalidTargetIDs(t *testing.T) {
	for _, tt := range []struct {
		name     string
		targetID any
	}{
		{name: "zero", targetID: "0"},
		{name: "negative", targetID: "-1"},
		{name: "fractional", targetID: "1.5"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/reminder", "range", testutil.Response{
				Body: map[string]any{"data": []any{
					map[string]any{"id": "1", "module": 4, "target_id": tt.targetID, "folder": "35", "alarm": 1784844000000},
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid target id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestReminderList_RejectsItemWithoutFolderID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "1", "module": 4, "target_id": "12", "alarm": 1784844000000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing folder rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestReminderList_RejectsInvalidFolderIDs(t *testing.T) {
	for _, tt := range []struct {
		name     string
		folderID any
	}{
		{name: "zero", folderID: "0"},
		{name: "negative", folderID: "-1"},
		{name: "fractional", folderID: "1.5"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/reminder", "range", testutil.Response{
				Body: map[string]any{"data": []any{
					map[string]any{"id": "1", "module": 4, "target_id": "12", "folder": tt.folderID, "alarm": 1784844000000},
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid folder id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestReminderList_RejectsItemWithoutModule(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "1", "target_id": "12", "folder": "35", "alarm": 1784844000000},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing module rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestReminderList_RejectsInvalidModules(t *testing.T) {
	for _, module := range []int{0, -1} {
		t.Run(strconv.Itoa(module), func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/reminder", "range", testutil.Response{
				Body: map[string]any{"data": []any{
					map[string]any{"id": "1", "module": module, "target_id": "12", "folder": "35", "alarm": 1784844000000},
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid module rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestReminderList_RejectsItemWithoutAlarm(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": "1", "module": 4, "target_id": "12", "folder": "35"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing alarm rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestReminderList_RejectsNonPositiveAlarms(t *testing.T) {
	for _, alarm := range []int64{0, -1} {
		t.Run(strconv.FormatInt(alarm, 10), func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/reminder", "range", testutil.Response{
				Body: map[string]any{"data": []any{
					map[string]any{"id": "1", "module": 4, "target_id": "12", "folder": "35", "alarm": alarm},
				}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want non-positive alarm rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestReminderList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

// TestReminderList_SendsOnlyTheDocumentedEndParam is the regression test for an
// undocumented start parameter: reminder?action=range takes only end (and modules).
func TestReminderList_SendsOnlyTheDocumentedEndParam(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var found bool
	for _, r := range srv.Requests() {
		if strings.Contains(r.URL.Path, "reminder") {
			found = true
			if r.URL.Query().Has("start") {
				t.Errorf("start = %q, want no start parameter", r.URL.Query().Get("start"))
			}
			if end := r.URL.Query().Get("end"); end != "1785542400000" {
				t.Errorf("end = %q, want epoch millis 1785542400000", end)
			}
		}
	}
	if !found {
		t.Fatal("no request to /ajax/reminder observed")
	}
}

// TestReminderList_FiltersFromBeforeLimit checks that --from is applied by the CLI and
// before --limit, so reminders due before --from neither appear nor use up the limit.
func TestReminderList_FiltersFromBeforeLimit(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": 1, "module": 4, "target_id": 11, "folder": 35, "alarm": 1782863999999},
		map[string]any{"id": 2, "module": 4, "target_id": 12, "folder": 35, "alarm": 1700000000000},
		map[string]any{"id": 3, "module": 4, "target_id": 13, "folder": 35, "alarm": 1782864000000},
		map[string]any{"id": 4, "module": 4, "target_id": 14, "folder": 35, "alarm": 1784844000000},
	}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--limit", "2", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"items":[{"id":"3","module":4,"target_id":"13","folder":"35","alarm":1782864000000},` +
		`{"id":"4","module":4,"target_id":"14","folder":"35","alarm":1784844000000}],"truncated":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestReminderList_BoundedLimit_Truncates(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	rows := make([]any, 0, 3)
	for i := 1; i <= 3; i++ {
		rows = append(rows, map[string]any{"id": i, "module": 4, "target_id": "1", "folder": "35", "alarm": 1784844000000})
	}
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": rows}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--limit", "2", "--llm",
	})

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

func TestReminderList_MissingFrom_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"reminder", "list", "--to", "2026-08-01T00:00:00Z", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--from") {
		t.Errorf("stderr = %q, want mention of --from", stderr)
	}
}

func TestReminderList_InvalidFrom_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"reminder", "list", "--from", "not-a-time", "--to", "2026-08-01T00:00:00Z", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--from") {
		t.Errorf("stderr = %q, want mention of --from", stderr)
	}
}

func TestReminderList_RejectsReversedRangeBeforeAuthentication(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"reminder", "list",
		"--from", "2026-08-01T00:00:00Z",
		"--to", "2026-07-01T00:00:00Z",
		"--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "after --from") {
		t.Fatalf("stderr = %q, want range-order error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("reversed range made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestReminderList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})

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

func TestReminderList_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"reminder", "bogus"})

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

// --- reminder delete ---

func TestReminderDelete_MissingConfirm_RefusesLocally(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"reminder", "delete", "--id", "1"})

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
		t.Errorf("expected no network requests without --confirm, got %d", len(srv.Requests()))
	}
}

func TestReminderDelete_ConfirmMismatch_RefusesLocally(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"reminder", "delete", "--id", "1", "--confirm", "2"})

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
		t.Errorf("expected no network requests on confirm mismatch, got %d", len(srv.Requests()))
	}
}

func TestReminderDelete_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "delete", "--id", "1", "--confirm", "1", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	for _, r := range srv.Requests() {
		if strings.Contains(r.URL.Path, "reminder") {
			t.Fatalf("dry-run sent a request to /ajax/reminder: %s", r.URL)
		}
	}
}

func TestReminderDelete_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"reminder", "delete", "--id", "1", "--confirm", "1", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	want := `{"deleted":true,"id":"1"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var sawDelete bool
	for _, r := range srv.Requests() {
		if strings.Contains(r.URL.Path, "reminder") && r.URL.Query().Get("action") == "delete" {
			sawDelete = true
			if r.Method != "PUT" {
				t.Errorf("method = %s, want PUT", r.Method)
			}
		}
	}
	if !sawDelete {
		t.Fatal("no delete request observed")
	}
}

func TestReminderDelete_SendsNumericArrayBody(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"reminder", "delete", "--id", "1", "--confirm", "1", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	for _, request := range srv.Requests() {
		if request.URL.Path != "/ajax/reminder" || request.URL.Query().Get("action") != "delete" {
			continue
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("reading delete request: %v", err)
		}
		if string(body) != `[{"id":1}]` {
			t.Fatalf("delete request body = %s, want numeric-id array", body)
		}
		return
	}
	t.Fatal("no delete request observed")
}

func TestReminderDelete_RejectsInvalidIDsBeforeNetwork(t *testing.T) {
	for _, id := range []string{"0", "-1", "not-a-number"} {
		t.Run(id, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/reminder", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{"reminder", "delete", "--id", id, "--confirm", id, "--llm"})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "positive integer") {
				t.Errorf("stderr = %q, want positive-integer error", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("invalid id made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

func TestReminderDelete_ReportsServerRefusal(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "delete", testutil.Response{Body: map[string]any{"data": []any{1}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"reminder", "delete", "--id", "1", "--confirm", "1", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want refused deletion reported; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "not deleted") {
		t.Errorf("stderr = %q, want deletion refusal", stderr)
	}
}

func TestReminderDelete_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "delete", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"reminder", "delete", "--id", "1", "--confirm", "1", "--llm"})

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

// --- reminder create ---

func TestReminderCreate_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 6}, "timestamp": 1785000000123}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": 90, "module": 4, "target_id": 5, "folder": 35, "alarm": 1784883600000},
		map[string]any{"id": 91, "module": 1, "target_id": 6, "folder": 35, "alarm": 1784883600000},
		map[string]any{"id": 92, "module": 4, "target_id": 6, "folder": 35, "alarm": 1784883600000},
	}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "2026-07-24T09:00:00Z", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	// id is the reminder id that reminder delete takes; task_id is the backing task.
	want := `{"id":"92","task_id":"6","folder_id":"35","alarm":1784883600000,"timestamp":1785000000123}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var sawNew, sawRange bool
	for _, r := range srv.Requests() {
		if strings.Contains(r.URL.Path, "tasks") && r.URL.Query().Get("action") == "new" {
			sawNew = true
			if r.Method != http.MethodPut {
				t.Errorf("method = %s, want PUT", r.Method)
			}
			var gotBody map[string]any
			if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
				t.Fatalf("decoding create request body: %v", err)
			}
			wantBody := map[string]any{
				"folder_id": "35", "title": "Call back", "alarm": float64(1784883600000),
			}
			if !reflect.DeepEqual(gotBody, wantBody) {
				t.Errorf("create request body = %#v, want %#v", gotBody, wantBody)
			}
		}
		if strings.Contains(r.URL.Path, "reminder") && r.URL.Query().Get("action") == "range" {
			sawRange = true
			end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
			if end < 1784883600000 || r.URL.Query().Has("start") {
				t.Errorf("range query = %s, want end >= alarm and no start", r.URL.RawQuery)
			}
		}
	}
	if !sawNew || !sawRange {
		t.Fatalf("tasks new seen = %v, reminder range seen = %v; want both", sawNew, sawRange)
	}
}

// TestReminderCreate_ReminderNotFound_ReportsTaskIDAndFails is the regression test for the
// reminder id mismatch: create used to print the task id as "id", which reminder delete
// does not accept. When no reminder row can be found, the task id must still reach the
// caller for cleanup, id is null, and the command fails.
func TestReminderCreate_ReminderNotFound_ReportsTaskIDAndFails(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 6}, "timestamp": 1785000000123}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": 90, "module": 4, "target_id": 6, "folder": 36, "alarm": 1784883600000},
	}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "2026-07-24T09:00:00Z", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	want := `{"id":null,"task_id":"6","folder_id":"35","alarm":1784883600000,"timestamp":1785000000123}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "reminder_not_found") || !strings.Contains(stderr, "tasks delete --id 6 --folder 35") {
		t.Errorf("stderr = %q, want reminder_not_found with cleanup guidance", stderr)
	}
}

func TestReminderCreate_AmbiguousReminderUsesAlarmTime(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "new", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 6}, "timestamp": 1785000000123}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": 90, "module": 4, "target_id": 6, "folder": 35, "alarm": 1784883000000},
		map[string]any{"id": 91, "module": 4, "target_id": 6, "folder": 35, "alarm": 1784883600000},
	}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "2026-07-24T09:00:00Z", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.HasPrefix(stdout, `{"id":"91","task_id":"6"`) {
		t.Errorf("stdout = %q, want the reminder whose alarm matches", stdout)
	}
}

func TestReminderCreate_RejectsNonPositiveTaskID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "new", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 0}, "timestamp": 1785000000123},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "2026-07-24T09:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestReminderCreate_MissingTitle_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"reminder", "create", "--folder", "35", "--at", "2026-07-24T09:00:00Z", "--llm",
	})

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

func TestReminderCreate_MissingFolder_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"reminder", "create", "--title", "Call back", "--at", "2026-07-24T09:00:00Z", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--folder") {
		t.Errorf("stderr = %q, want mention of --folder", stderr)
	}
}

func TestReminderCreate_MissingAt_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--at") {
		t.Errorf("stderr = %q, want mention of --at", stderr)
	}
}

func TestReminderCreate_InvalidAt_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "not-a-time", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--at") {
		t.Errorf("stderr = %q, want mention of --at", stderr)
	}
}

func TestReminderCreate_NonPositiveAt_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "1970-01-01T00:00:00Z", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "invalid_argument") || !strings.Contains(stderr, "--at") {
		t.Errorf("stderr = %q, want invalid_argument mentioning --at", stderr)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestReminderCreate_DryRun_NoRequestSent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "2026-07-24T09:00:00Z", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run:true", stdout)
	}
	for _, r := range srv.Requests() {
		if strings.Contains(r.URL.Path, "tasks") {
			t.Fatalf("dry-run sent a request to /ajax/tasks: %s", r.URL)
		}
	}
}

func TestReminderCreate_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/tasks", "new", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"reminder", "create", "--title", "Call back", "--folder", "35", "--at", "2026-07-24T09:00:00Z", "--llm",
	})

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

func TestDescribe_LLM_ReminderCreate(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "reminder.create", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"reminder.create"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"write"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestDescribe_LLM_ReminderList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "reminder.list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"reminder.list"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
}

func TestDescribe_LLM_ReminderDelete(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "reminder.delete", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"effect":"destructive"`) {
		t.Errorf("stdout = %q, want effect field destructive", stdout)
	}
}

func TestReminder_DryRunFieldsMatchTheDryRunResults(t *testing.T) {
	assertDryRunFieldsAreRegistered(t, "reminder.create",
		[]string{"reminder", "create", "--title", "t", "--folder", "30", "--at", "2026-07-24T09:00:00Z"})
	assertDryRunFieldsAreRegistered(t, "reminder.delete",
		[]string{"reminder", "delete", "--id", "5", "--confirm", "5"})
}
