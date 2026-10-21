// ABOUTME: CLI integration tests for the events list command: human and LLM output,
// ABOUTME: validation, authentication failures, API failures, and stdout/stderr/exit-code separation.
package cli

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestEventsList_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "all", testutil.Response{
		Body: map[string]any{"data": []map[string]any{
			{"id": "evt-1", "summary": "Standup", "startDate": map[string]any{"value": "20260701T090000Z"}, "endDate": map[string]any{"value": "20260701T093000Z"}},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if strings.Contains(stdout, "  ") {
		t.Errorf("stdout not compact: %q", stdout)
	}
	if !strings.Contains(stdout, `"evt-1"`) {
		t.Errorf("stdout = %q, want event id present", stdout)
	}
	if !strings.Contains(stdout, `"truncated":false`) {
		t.Errorf("stdout = %q, want truncated field", stdout)
	}

	reqs := srv.Requests()
	var chronosReq bool
	for _, r := range reqs {
		if r.URL.Path == "/ajax/chronos" {
			chronosReq = true
			if got := r.URL.Query().Get("rangeStart"); got != "20260701T000000Z" {
				t.Errorf("rangeStart = %q, want 20260701T000000Z", got)
			}
			if got := r.URL.Query().Get("rangeEnd"); got != "20260801T000000Z" {
				t.Errorf("rangeEnd = %q, want 20260801T000000Z", got)
			}
		}
	}
	if !chronosReq {
		t.Fatal("no request made to /ajax/chronos")
	}
}

func TestEventsList_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "all", testutil.Response{
		Body: map[string]any{"data": []map[string]any{
			{"id": "evt-1", "summary": "Standup"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "evt-1") || !strings.Contains(stdout, "Standup") {
		t.Errorf("stdout = %q, want event id and summary present", stdout)
	}
}

func TestEventsList_EmptyResult(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": []map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"items":[],"count":0,"truncated":false}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestEventsList_Truncation(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	items := make([]map[string]any, 3)
	for i := range items {
		items[i] = map[string]any{"id": "evt", "summary": "x"}
	}
	srv.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": items}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--limit", "2", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"count":2`) {
		t.Errorf("stdout = %q, want count 2", stdout)
	}
	if !strings.Contains(stdout, `"truncated":true`) {
		t.Errorf("stdout = %q, want truncated true", stdout)
	}
}

func TestEventsList_MissingFrom_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "list", "--to", "2026-08-01T00:00:00Z", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--from") {
		t.Errorf("stderr = %q, want mention of --from", stderr)
	}
}

func TestEventsList_InvalidFromFormat_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "list", "--from", "not-a-date", "--to", "2026-08-01T00:00:00Z", "--llm"})

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

func TestEventsList_RejectsReversedRangeBeforeAuthentication(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"events", "list",
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

func TestEventsList_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "list", "--bogus"})

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

func TestEventsList_UnknownAction_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "bogus"})

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

func TestEventsList_MissingCredentials_LLMStructuredError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm"})

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

func TestEventsList_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "all", testutil.Response{
		Body: map[string]any{
			"error": "Your session expired. Please login again.", "code": "SES-0203", "categories": "TRY_AGAIN",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm"})

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

func TestEventsList_CredentialsNeverAppearInOutput(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{
		Body: map[string]any{
			"error": "bad creds", "code": "LGI-0006", "categories": "USER_INPUT",
		},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "s3cr3tpassword"}
	stdout, stderr, _ := run(t, srv, env, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z"})

	if strings.Contains(stdout, "s3cr3tpassword") || strings.Contains(stderr, "s3cr3tpassword") {
		t.Fatal("password leaked into output")
	}
}

func TestDescribe_LLM_EventsList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "events.list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"events.list"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect field", stdout)
	}
	assertGolden(t, "testdata/describe_events_list_llm.golden", stdout)
}

func TestCommands_LLM_ListsEventsList(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "events", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"name":"events.list"`) {
		t.Errorf("stdout = %q, want events.list listed", stdout)
	}
}

// --- events.get ---

func TestEventsGet_LLMMode_CompactJSON(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "evt-1", "summary": "Standup",
			"startDate": map[string]any{"value": "20260701T090000Z"},
			"endDate":   map[string]any{"value": "20260701T093000Z"},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "get", "--id", "evt-1", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"evt-1"`) || !strings.Contains(stdout, `"Standup"`) {
		t.Errorf("stdout = %q, want event id and summary", stdout)
	}

	reqs := srv.Requests()
	for _, r := range reqs {
		if r.URL.Path == "/ajax/chronos" {
			if got := r.URL.Query().Get("id"); got != "evt-1" {
				t.Errorf("id = %q, want evt-1", got)
			}
			if got := r.URL.Query().Get("folder"); got != "cal://0/32" {
				t.Errorf("folder = %q, want cal://0/32", got)
			}
		}
	}
}

func TestEventsGet_OptionalFieldsRemainSelectable(t *testing.T) {
	for _, field := range []string{"summary", "startDate", "endDate", "description", "location"} {
		t.Run(field, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/chronos", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{"id": "evt-1"}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"events", "get", "--id", "evt-1", "--format", "json", "--select", field,
			})

			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if !strings.Contains(stdout, `"`+field+`"`) {
				t.Fatalf("stdout = %q, want selected %s field", stdout, field)
			}
		})
	}
}

func TestEventsGet_HumanMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1", "summary": "Standup"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "get", "--id", "evt-1"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, "evt-1") || !strings.Contains(stdout, "Standup") {
		t.Errorf("stdout = %q, want event id and summary present", stdout)
	}
}

func TestEventsGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-2", "summary": "Wrong event"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "get", "--id", "evt-1", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestEventsGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "get", "--llm"})

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

func TestEventsGet_CustomFolder(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1", "summary": "Standup"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, _, code := run(t, srv, env, []string{"events", "get", "--id", "evt-1", "--folder", "99", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}

	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/chronos" {
			if got := r.URL.Query().Get("folder"); got != "cal://0/99" {
				t.Errorf("folder = %q, want cal://0/99", got)
			}
		}
	}
}

func TestEventsGet_NotFound_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"error": "not found", "code": "CAL-4040", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "get", "--id", "bogus", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "CAL-4040") {
		t.Errorf("stderr = %q, want CAL-4040 code", stderr)
	}
}

func TestDescribe_LLM_EventsGet(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "events.get", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"effect":"read"`) {
		t.Errorf("stdout = %q, want effect:read", stdout)
	}
}

// --- events.create ---

func TestEventsCreate_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	// Confirmed live: PUT /chronos?action=new responds with a CalendarResult
	// ({"created":[...],"updated":[],"deleted":[]}), not a bare event object, since a
	// single create request can in principle affect multiple occurrences.
	srv.Handle("/ajax/chronos", "new", testutil.Response{Body: map[string]any{"data": map[string]any{
		"created": []any{map[string]any{"id": "evt-new"}},
		"updated": []any{},
		"deleted": []any{},
	}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "create",
		"--summary", "Standup",
		"--start", "2026-07-01T09:00:00Z",
		"--end", "2026-07-01T09:30:00Z",
		"--description", "Daily planning",
		"--location", "Room 4",
		"--folder", "99",
		"--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"id":"evt-new"}`+"\n" {
		t.Errorf("stdout = %q, want exact created event identity", stdout)
	}

	wantBody := map[string]any{
		"summary":     "Standup",
		"startDate":   map[string]any{"value": "20260701T090000Z", "tzid": "UTC"},
		"endDate":     map[string]any{"value": "20260701T093000Z", "tzid": "UTC"},
		"description": "Daily planning",
		"location":    "Room 4",
	}
	var body map[string]any
	var sawCreate bool
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/chronos" && r.URL.Query().Get("action") == "new" {
			sawCreate = true
			if r.Method != http.MethodPut {
				t.Errorf("method = %q, want PUT", r.Method)
			}
			if got := r.URL.Query().Get("folder"); got != "cal://0/99" {
				t.Errorf("folder = %q, want cal://0/99", got)
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
		}
	}
	if !sawCreate {
		t.Fatal("no create request made")
	}
	if !reflect.DeepEqual(body, wantBody) {
		t.Errorf("body = %#v, want %#v", body, wantBody)
	}
}

func TestEventsCreate_MissingSummary_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"events", "create", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T09:30:00Z", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--summary") {
		t.Errorf("stderr = %q, want mention of --summary", stderr)
	}
}

func TestEventsCreate_InvalidStart_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"events", "create", "--summary", "x", "--start", "nope", "--end", "2026-07-01T09:30:00Z", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--start") {
		t.Errorf("stderr = %q, want mention of --start", stderr)
	}
}

func TestEventsCreate_RejectsEndBeforeStart(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"events", "create", "--summary", "Backwards",
		"--start", "2026-07-01T10:00:00Z",
		"--end", "2026-07-01T09:30:00Z",
		"--dry-run", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "after --start") {
		t.Fatalf("stderr = %q, want event interval error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid interval made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestEventsCreate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "create",
		"--summary", "Standup",
		"--start", "2026-07-01T09:00:00Z",
		"--end", "2026-07-01T09:30:00Z",
		"--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls for --dry-run, got %d", len(srv.Requests()))
	}
}

func TestEventsCreate_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "new", testutil.Response{
		Body: map[string]any{"error": "bad summary", "code": "CAL-1000", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "create", "--summary", "x", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T09:30:00Z", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "CAL-1000") {
		t.Errorf("stderr = %q, want CAL-1000 code", stderr)
	}
}

// --- events.update ---

func TestEventsUpdate_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1", "summary": "Standup"}, "timestamp": 1000},
	})
	srv.Handle("/ajax/chronos", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": "evt-1"}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "update", "--id", "evt-1", "--folder", "99",
		"--summary", "Renamed",
		"--start", "2026-07-01T10:00:00Z",
		"--end", "2026-07-01T10:45:00Z",
		"--description", "Updated planning",
		"--location", "Room 5",
		"--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"id":"evt-1"}`+"\n" {
		t.Errorf("stdout = %q, want exact updated event identity", stdout)
	}

	wantBody := map[string]any{
		"summary":     "Renamed",
		"startDate":   map[string]any{"value": "20260701T100000Z", "tzid": "UTC"},
		"endDate":     map[string]any{"value": "20260701T104500Z", "tzid": "UTC"},
		"description": "Updated planning",
		"location":    "Room 5",
	}
	var sawGet, sawUpdate bool
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/chronos" && r.URL.Query().Get("action") == "get" {
			sawGet = true
			if r.Method != http.MethodGet {
				t.Errorf("preflight method = %q, want GET", r.Method)
			}
			if got := r.URL.Query().Get("id"); got != "evt-1" {
				t.Errorf("preflight id = %q, want evt-1", got)
			}
			if got := r.URL.Query().Get("folder"); got != "cal://0/99" {
				t.Errorf("preflight folder = %q, want cal://0/99", got)
			}
		}
		if r.URL.Path == "/ajax/chronos" && r.URL.Query().Get("action") == "update" {
			sawUpdate = true
			if r.Method != http.MethodPut {
				t.Errorf("update method = %q, want PUT", r.Method)
			}
			if got := r.URL.Query().Get("timestamp"); got != "1000" {
				t.Errorf("timestamp = %q, want 1000", got)
			}
			if got := r.URL.Query().Get("id"); got != "evt-1" {
				t.Errorf("id = %q, want evt-1", got)
			}
			if got := r.URL.Query().Get("folder"); got != "cal://0/99" {
				t.Errorf("folder = %q, want cal://0/99", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode update body: %v", err)
			}
			if !reflect.DeepEqual(body, wantBody) {
				t.Errorf("body = %#v, want %#v", body, wantBody)
			}
		}
	}
	if !sawGet {
		t.Fatal("no timestamp preflight request made")
	}
	if !sawUpdate {
		t.Fatal("no update request made")
	}
}

func TestEventsUpdate_RejectsMismatchedResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 1000},
	})
	srv.Handle("/ajax/chronos", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-2"}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "update", "--id", "evt-1", "--summary", "Renamed", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched update response rejected; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
}

func TestEventsUpdate_AcceptsDocumentedEmptyResponse(t *testing.T) {
	for name, responseData := range map[string]any{
		"empty object": map[string]any{},
		"null":         nil,
	} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/chronos", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 1000},
			})
			srv.Handle("/ajax/chronos", "update", testutil.Response{
				Body: map[string]any{"data": responseData},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"events", "update", "--id", "evt-1", "--summary", "Renamed", "--llm",
			})

			if code != 0 {
				t.Fatalf("exit code = %d, want documented empty update success; stderr=%s", code, stderr)
			}
			if stdout != `{"id":"evt-1"}`+"\n" {
				t.Errorf("stdout = %q, want requested event identity", stdout)
			}
		})
	}
}

func TestEventsUpdate_AcceptsCalendarResultWithMatchingUpdatedEvent(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 1000},
	})
	srv.Handle("/ajax/chronos", "update", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"updated":   []any{map[string]any{"id": "evt-1", "summary": "Renamed"}},
			"deleted":   []any{},
			"conflicts": []any{},
		}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "update", "--id", "evt-1", "--summary", "Renamed", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want CalendarResult accepted; stderr=%s", code, stderr)
	}
	if stdout != `{"id":"evt-1"}`+"\n" {
		t.Errorf("stdout = %q, want requested event identity", stdout)
	}
}

func TestEventsUpdate_RejectsNonEmptyResponseWithoutMatchingIdentity(t *testing.T) {
	for name, responseData := range map[string]any{
		"missing id":                  map[string]any{"timestamp": 123},
		"empty updated collection":    map[string]any{"updated": []any{}, "deleted": []any{}, "conflicts": []any{}},
		"mismatched updated identity": map[string]any{"updated": []any{map[string]any{"id": "evt-2"}}},
		"wrong shape":                 "evt-1",
	} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/chronos", "get", testutil.Response{
				Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 1000},
			})
			srv.Handle("/ajax/chronos", "update", testutil.Response{
				Body: map[string]any{"data": responseData},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"events", "update", "--id", "evt-1", "--summary", "Renamed", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid update result rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Errorf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestEventsUpdate_MissingID_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "update", "--summary", "x", "--llm"})

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

func TestEventsUpdate_NoFieldsGiven_InvalidArgumentError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"events", "update", "--id", "evt-1", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "at least one") {
		t.Errorf("stderr = %q, want mention of required field", stderr)
	}
}

func TestEventsUpdate_RejectsEndBeforeStart(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"events", "update", "--id", "evt-1",
		"--start", "2026-07-01T10:00:00Z",
		"--end", "2026-07-01T09:30:00Z",
		"--dry-run", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "after --start") {
		t.Fatalf("stderr = %q, want event interval error", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("invalid interval made %d HTTP requests, want 0", len(srv.Requests()))
	}
}

func TestEventsUpdate_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "update", "--id", "evt-1", "--summary", "Renamed", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls for --dry-run, got %d", len(srv.Requests()))
	}
}

func TestEventsUpdate_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 1000},
	})
	srv.Handle("/ajax/chronos", "update", testutil.Response{
		Body: map[string]any{"error": "conflict", "code": "CAL-4020", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "update", "--id", "evt-1", "--summary", "x", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "CAL-4020") {
		t.Errorf("stderr = %q, want CAL-4020 code", stderr)
	}
}

// --- events.delete ---

func TestEventsDelete_MissingConfirm_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "delete", "--id", "evt-1", "--llm"})

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
		t.Errorf("expected no network calls without --confirm, got %d", len(srv.Requests()))
	}
}

func TestEventsDelete_ConfirmMismatch_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "delete", "--id", "evt-1", "--confirm", "evt-wrong", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "--confirm") {
		t.Errorf("stderr = %q, want mention of --confirm mismatch", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls on confirm mismatch, got %d", len(srv.Requests()))
	}
}

func TestEventsDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("expected no network calls for --dry-run, got %d", len(srv.Requests()))
	}
}

func TestEventsDelete_LLMMode_Success(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 2000},
	})
	srv.Handle("/ajax/chronos", "delete", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{
			"folderId": "cal://0/99",
			"id":       "evt-1",
			"deleted":  []any{map[string]any{"id": "evt-1", "folder": "cal://0/99"}},
		},
	}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "delete", "--id", "evt-1", "--folder", "99", "--confirm", "evt-1", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if stdout != `{"deleted":true,"id":"evt-1"}`+"\n" {
		t.Errorf("stdout = %q, want exact deletion identity", stdout)
	}

	var sawGet, sawDelete bool
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/chronos" && r.URL.Query().Get("action") == "get" {
			sawGet = true
			if r.Method != http.MethodGet {
				t.Errorf("preflight method = %q, want GET", r.Method)
			}
			if got := r.URL.Query().Get("id"); got != "evt-1" {
				t.Errorf("preflight id = %q, want evt-1", got)
			}
			if got := r.URL.Query().Get("folder"); got != "cal://0/99" {
				t.Errorf("preflight folder = %q, want cal://0/99", got)
			}
		}
		if r.URL.Path == "/ajax/chronos" && r.URL.Query().Get("action") == "delete" {
			sawDelete = true
			if r.Method != http.MethodPut {
				t.Errorf("delete method = %q, want PUT", r.Method)
			}
			if got := r.URL.Query().Get("timestamp"); got != "2000" {
				t.Errorf("timestamp = %q, want 2000", got)
			}
			var body []map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode delete body: %v", err)
			}
			wantBody := []map[string]any{{"id": "evt-1", "folder": "cal://0/99"}}
			if !reflect.DeepEqual(body, wantBody) {
				t.Errorf("body = %#v, want %#v", body, wantBody)
			}
		}
	}
	if !sawGet {
		t.Fatal("no timestamp preflight request made")
	}
	if !sawDelete {
		t.Fatal("no delete request made")
	}
}

func TestEventsDelete_RejectsMismatchedTimestampResponseID(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-2"}, "timestamp": 2000},
	})
	srv.Handle("/ajax/chronos", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched timestamp response rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/chronos" && req.URL.Query().Get("action") == "delete" {
			t.Fatal("delete request sent after timestamp lookup returned a different event")
		}
	}
}

func TestEventsDelete_APIError(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 2000},
	})
	srv.Handle("/ajax/chronos", "delete", testutil.Response{
		Body: map[string]any{"error": "not found", "code": "CAL-4040", "categories": "USER_INPUT"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "CAL-4040") {
		t.Errorf("stderr = %q, want CAL-4040 code", stderr)
	}
}

func TestEventsDelete_NeverAutoRetries(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "evt-1"}, "timestamp": 2000},
	})
	srv.Handle("/ajax/chronos", "delete", testutil.Response{
		Body: map[string]any{"error": "conflict", "code": "CAL-4020", "categories": "TRY_AGAIN"},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, _, code := run(t, srv, env, []string{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}

	var deleteAttempts int
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/chronos" && r.URL.Query().Get("action") == "delete" {
			deleteAttempts++
		}
	}
	if deleteAttempts != 1 {
		t.Errorf("delete attempts = %d, want exactly 1 (no auto-retry)", deleteAttempts)
	}
}

func TestDescribe_LLM_EventsDelete(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "events.delete", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want empty", stderr)
	}
	if !strings.Contains(stdout, `"effect":"destructive"`) {
		t.Errorf("stdout = %q, want effect:destructive", stdout)
	}
}

// assertDryRunFieldsAreRegistered runs each argument list with --dry-run --llm and checks
// the top-level fields of the result against the DryRunFields registered for operation.
// Each run may write only registered fields, and together the runs must write all of
// them, so --select with --dry-run is checked against the shape the dry run really has.
func assertDryRunFieldsAreRegistered(t *testing.T, operation string, argLists ...[]string) {
	t.Helper()
	op, ok := operations.Default.Get(operation)
	if !ok {
		t.Fatalf("%s is not registered", operation)
	}
	registered := stringSet(op.Output.DryRunFields...)
	seen := map[string]bool{}
	for _, args := range argLists {
		srv := testutil.NewServer()
		stdout, stderr, code := run(t, srv, map[string]string{}, append(append([]string(nil), args...), "--dry-run", "--llm"))
		requests := len(srv.Requests())
		srv.Close()
		if code != 0 || requests != 0 {
			t.Fatalf("%v: exit=%d requests=%d stderr=%q, want a local dry run", args, code, requests, stderr)
		}
		var result map[string]json.RawMessage
		if err := json.Unmarshal([]byte(stdout), &result); err != nil {
			t.Fatalf("%v: stdout %q is not a JSON object: %v", args, stdout, err)
		}
		for field := range result {
			if !registered[field] {
				t.Errorf("%v: dry run writes %q, which %s does not register in DryRunFields", args, field, operation)
			}
			seen[field] = true
		}
	}
	var missing []string
	for _, field := range op.Output.DryRunFields {
		if !seen[field] {
			missing = append(missing, field)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("%s registers dry-run fields %v that no dry run wrote", operation, missing)
	}
}

func TestEvents_DryRunFieldsMatchTheDryRunResults(t *testing.T) {
	icsPath := filepath.Join(t.TempDir(), "event.ics")
	if err := os.WriteFile(icsPath, []byte("BEGIN:VCALENDAR\nEND:VCALENDAR\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertDryRunFieldsAreRegistered(t, "events.create",
		[]string{"events", "create", "--summary", "s", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T10:00:00Z"})
	assertDryRunFieldsAreRegistered(t, "events.update",
		[]string{"events", "update", "--id", "5", "--summary", "s", "--recurrence-id", "20260708T090000Z"})
	assertDryRunFieldsAreRegistered(t, "events.delete",
		[]string{"events", "delete", "--id", "5", "--confirm", "5", "--recurrence-id", "20260708T090000Z"},
		[]string{"events", "delete", "--id", "5", "--confirm", "5", "--series"})
	assertDryRunFieldsAreRegistered(t, "events.import",
		[]string{"events", "import", "--file", icsPath})
}

func TestEvents_SelectIsCheckedBeforeTheCommandRuns(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	stdout, stderr, code := run(t, srv, env, []string{
		"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--select", "summary.text",
	})
	if code != 4 || stdout != "" || !strings.Contains(stderr, `holds a single value`) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want a scalar-field error", code, stdout, stderr)
	}

	stdout, stderr, code = run(t, srv, env, []string{
		"events", "create", "--summary", "s", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T10:00:00Z",
		"--dry-run", "--select", "id",
	})
	if code != 4 || stdout != "" || !strings.Contains(stderr, "dry-run fields: dry_run, event, folder, timezone") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want a dry-run field error", code, stdout, stderr)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Fatalf("rejected --select sent %d requests", n)
	}
}
