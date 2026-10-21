// ABOUTME: CLI integration tests for events export/import: iCal text passthrough,
// ABOUTME: --output file writing, multipart upload, and per-item error surfacing.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

const testICal = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nEND:VCALENDAR\r\n"

func eventsIOTestServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	return srv
}

func TestEventsExport_HumanMode_WritesRawICalToStdout(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/export", "ICAL", testutil.Response{Body: []byte(testICal)})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "export", "--folder", "32"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != testICal {
		t.Errorf("stdout = %q, want raw iCal %q", stdout, testICal)
	}
}

func TestEventsExport_DefaultFolder_WhenOmitted(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/export", "ICAL", testutil.Response{Body: []byte(testICal)})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"events", "export"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var found bool
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/export" {
			found = true
			if r.URL.Query().Get("folder") != defaultCalendarFolder {
				t.Errorf("folder = %q, want %s", r.URL.Query().Get("folder"), defaultCalendarFolder)
			}
		}
	}
	if !found {
		t.Fatal("no request to /ajax/export observed")
	}
}

func TestEventsExport_Output_WritesFileAndPrintsConfirmation(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/export", "ICAL", testutil.Response{Body: []byte(testICal)})

	dst := filepath.Join(t.TempDir(), "calendar.ics")
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "export", "--folder", "32", "--output", dst, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("could not read --output file: %v", err)
	}
	if string(content) != testICal {
		t.Errorf("file content = %q, want %q", content, testICal)
	}
	if !strings.Contains(stdout, `"saved_to":`) || !strings.Contains(stdout, dst) {
		t.Errorf("stdout = %q, want saved_to mentioning %s", stdout, dst)
	}
}

func TestEventsImport_Success(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "ICAL", testutil.Response{
		Body: map[string]any{"data": []map[string]any{{"id": "3", "folder_id": "32", "last_modified": 1784857972966}}},
	})

	src := filepath.Join(t.TempDir(), "in.ics")
	if err := os.WriteFile(src, []byte(testICal), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "import", "--file", src, "--folder", "32", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"3","folder_id":"32","items":[{"index":0,"id":"3","folder_id":"32"}],"count":1,"failed":[],"failed_count":0}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	assertImportMultipartRequest(t, srv, "ICAL", "32", "in.ics", "text/calendar", testICal)
}

func TestEventsImport_RejectsIncompleteSuccessResult(t *testing.T) {
	tests := []struct {
		name   string
		result map[string]any
	}{
		{name: "missing id", result: map[string]any{"folder_id": "32"}},
		{name: "missing folder id", result: map[string]any{"id": "3"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := eventsIOTestServer()
			defer srv.Close()
			srv.Handle("/ajax/import", "ICAL", testutil.Response{
				Body: map[string]any{"data": []map[string]any{tt.result}},
			})

			src := filepath.Join(t.TempDir(), "in.ics")
			if err := os.WriteFile(src, []byte(testICal), 0o600); err != nil {
				t.Fatal(err)
			}

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"events", "import", "--file", src, "--folder", "32", "--llm",
			})

			if code == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestEventsImport_RejectsMismatchedResultFolder(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "ICAL", testutil.Response{
		Body: map[string]any{"data": []map[string]any{{"id": "3", "folder_id": "31", "last_modified": 1784857972966}}},
	})

	src := filepath.Join(t.TempDir(), "in.ics")
	if err := os.WriteFile(src, []byte(testICal), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "import", "--file", src, "--folder", "32", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched result folder rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestEventsImport_PerItemError_ReportedOnStdoutWithNonzeroExit(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "ICAL", testutil.Response{
		Body: map[string]any{"data": []map[string]any{{"error": "Invalid iCal", "code": "ICAL-0001"}}},
	})

	src := filepath.Join(t.TempDir(), "in.ics")
	if err := os.WriteFile(src, []byte(testICal), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "import", "--file", src, "--folder", "32", "--llm"})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"id":null,"folder_id":null,"items":[],"count":0,"failed":[{"index":0,"code":"ICAL-0001","message":"Invalid iCal"}],"failed_count":1}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "import_incomplete") || !strings.Contains(stderr, "Invalid iCal") {
		t.Errorf("stderr = %q, want import_incomplete with the validation error message", stderr)
	}
}

func TestEventsImport_MissingFile_InvalidArgumentError(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "import", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--file") {
		t.Errorf("stderr = %q, want mention of --file", stderr)
	}
}

func TestEventsImport_DryRun_NoNetworkCall(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "in.ics")
	if err := os.WriteFile(src, []byte(testICal), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "import", "--file", src, "--folder", "32", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/import" {
			t.Fatalf("unexpected request to /ajax/import: %s", r.URL)
		}
	}
}

// TestEventsExport_StructuredFormatWithoutOutput_InvalidArgument is the regression test for
// an export that broke its own output contract: without --output the iCal document went to
// stdout verbatim, so --llm produced something that is not JSON and --format tsv/jsonl/values
// failed downstream with "command did not return structured JSON".
func TestEventsExport_StructuredFormatWithoutOutput_InvalidArgument(t *testing.T) {
	for _, format := range []string{"json", "tsv", "jsonl", "values"} {
		t.Run(format, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/export", "ICAL", testutil.Response{Body: []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			args := []string{"events", "export", "--format", format}
			if format != "json" {
				args = append(args, "--select", "saved_to")
			}
			stdout, stderr, code := run(t, srv, env, args)

			if code == 0 {
				t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
			}
			if strings.Contains(stdout, "BEGIN:VCALENDAR") {
				t.Errorf("stdout = %q, want no raw iCal in structured mode", stdout)
			}
			if !strings.Contains(stderr, "--output") {
				t.Errorf("stderr = %q, want the caller pointed at --output", stderr)
			}
		})
	}
}

// TestEventsExport_HumanModeWithoutOutput_StreamsICal pins that the human path still writes
// the document straight to stdout, which is what makes redirection work.
func TestEventsExport_HumanModeWithoutOutput_StreamsICal(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/export", "ICAL", testutil.Response{Body: []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "export"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "BEGIN:VCALENDAR") {
		t.Errorf("stdout = %q, want the iCal document", stdout)
	}
}

// TestEventsExport_StructuredFormatWithOutput_ReturnsDocumentedFields pins that the
// documented saved_to/bytes_written object is what structured callers get.
func TestEventsExport_StructuredFormatWithOutput_ReturnsDocumentedFields(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/export", "ICAL", testutil.Response{Body: []byte("BEGIN:VCALENDAR\r\nEND:VCALENDAR\r\n")})

	out := filepath.Join(t.TempDir(), "cal.ics")
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "export", "--output", out, "--format", "tsv", "--select", "saved_to"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if want := "saved_to\n" + out + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

// TestEventsImport_RejectionAfterTheFirstEntry_SurfacedAsAPIError is the calendar half of
// the partial-import regression: an .ics file holds one or more events and the server answers
// with one result per event, but only the first result was inspected.
// TestEventsImport_RejectionAfterTheFirstEntry_ReportsStoredAndFailed is the regression
// test for orphaned imports (audit F-03): the stored event 42 must stay in the report when
// the second entry is rejected.
func TestEventsImport_RejectionAfterTheFirstEntry_ReportsStoredAndFailed(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "ICAL", testutil.Response{
		Body: map[string]any{"data": []map[string]any{
			{"id": "42", "folder_id": "32", "last_modified": 1784857952353},
			{"error": "Bad iCal: DTSTART is missing", "code": "CAL-4221"},
		}},
	})

	src := filepath.Join(t.TempDir(), "in.ics")
	if err := os.WriteFile(src, []byte(testICal), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "import", "--file", src, "--folder", "32", "--llm"})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q", code, stdout)
	}
	want := `{"id":"42","folder_id":"32","items":[{"index":0,"id":"42","folder_id":"32"}],"count":1,` +
		`"failed":[{"index":1,"code":"CAL-4221","message":"Bad iCal: DTSTART is missing"}],"failed_count":1}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "import_incomplete") || !strings.Contains(stderr, "DTSTART is missing") {
		t.Errorf("stderr = %q, want import_incomplete with the rejected entry's message", stderr)
	}
}

func TestEventsImport_EveryEntryAccepted_ReturnsEveryIdentity(t *testing.T) {
	srv := eventsIOTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "ICAL", testutil.Response{
		Body: map[string]any{"data": []map[string]any{
			{"id": "42", "folder_id": "32", "last_modified": 1784857952353},
			{"id": "43", "folder_id": "32", "last_modified": 1784857952354},
		}},
	})

	src := filepath.Join(t.TempDir(), "in.ics")
	if err := os.WriteFile(src, []byte(testICal), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"events", "import", "--file", src, "--folder", "32", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"42","folder_id":"32","items":[{"index":0,"id":"42","folder_id":"32"},{"index":1,"id":"43","folder_id":"32"}],"count":2,"failed":[],"failed_count":0}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want every imported event identity", stdout)
	}
}
