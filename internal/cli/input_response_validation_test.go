// ABOUTME: Tests for structured failures, strict JSON input handling, upload safety,
// ABOUTME: and mutation response validation.
package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"runtime"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func decodeStructuredError(t *testing.T, stderr string) (code, message string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatalf("stderr = %q, want structured error JSON: %v", stderr, err)
	}
	if envelope.Error.Code == "" || envelope.Error.Message == "" {
		t.Fatalf("stderr = %q, want non-empty error code and message", stderr)
	}
	return envelope.Error.Code, envelope.Error.Message
}

func TestStructuredErrors_SurviveEarlyParsingAndDispatchFailures(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantMessage string
	}{
		{
			name:        "global output parser fails after llm flag",
			args:        []string{"whoami", "--llm", "--format"},
			wantMessage: "--format requires a value",
		},
		{
			name:        "llm flag follows an invalid format value",
			args:        []string{"whoami", "--format", "--llm"},
			wantMessage: "unknown format",
		},
		{
			name:        "handler fails before normalized llm marker",
			args:        []string{"whoami", "--bogus", "--llm"},
			wantMessage: "unknown flag: --bogus",
		},
		{
			name:        "resource is missing its action",
			args:        []string{"contacts", "--llm"},
			wantMessage: "contacts requires an action",
		},
		{
			name:        "normalized llm marker is not consumed as a missing flag value",
			args:        []string{"mail", "list", "--folder", "--format", "json"},
			wantMessage: "--folder requires a value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, exitCode := run(t, srv, map[string]string{}, tt.args)
			if exitCode == 0 {
				t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			code, message := decodeStructuredError(t, stderr)
			if code != "invalid_argument" {
				t.Errorf("error code = %q, want invalid_argument", code)
			}
			if !strings.Contains(message, tt.wantMessage) {
				t.Errorf("error message = %q, want %q", message, tt.wantMessage)
			}
		})
	}
}

func TestDescribe_RejectsExtraPositionalArguments(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "whoami", "extra", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	_, message := decodeStructuredError(t, stderr)
	if !strings.Contains(message, "extra") {
		t.Errorf("error message = %q, want unexpected argument named", message)
	}
}

func TestRunStructured_DoesNotLeakPartialOutputWhenTransformFails(t *testing.T) {
	var input strings.Builder
	input.WriteByte('[')
	for i := 0; i < 2000; i++ {
		if i > 0 {
			input.WriteByte(',')
		}
		input.WriteString(`{"first":"a","second":"b"}`)
	}
	input.WriteString(`,7]`)

	var stdout, stderr bytes.Buffer
	code := runStructured(&stdout, &stderr, outputOptions{structured: true, format: render.FormatTSV}, func(out, _ io.Writer) int {
		_, _ = io.WriteString(out, input.String())
		return 0
	})
	if code == 0 {
		t.Fatal("exit code = 0, want heterogeneous TSV conversion failure")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout contains %d partial bytes on failure, want empty", stdout.Len())
	}
	errCode, _ := decodeStructuredError(t, stderr.String())
	if errCode != "invalid_argument" {
		t.Errorf("error code = %q, want invalid_argument", errCode)
	}
}

func TestParseInputValue_PreservesLargeJSONIntegers(t *testing.T) {
	fields, err := buildContactFields(`{"folder_id":9007199254740993}`, nil)
	if err != nil {
		t.Fatalf("buildContactFields() error = %v", err)
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshaling parsed fields: %v", err)
	}
	if got, want := string(encoded), `{"folder_id":9007199254740993}`; got != want {
		t.Errorf("marshaled fields = %s, want exact integer %s", got, want)
	}
	if got := contactFieldString(fields["folder_id"]); got != "9007199254740993" {
		t.Errorf("folder id = %q, want exact integer", got)
	}
}

func TestParseInputValue_RejectsNullInsteadOfTreatingItAsAnEmptyObject(t *testing.T) {
	if _, err := parseInputValue("null"); err == nil {
		t.Fatal("parseInputValue(null) error = nil, want JSON object error")
	}
}

func TestParseBatchInput_PreservesNumbersAndRejectsUnknownFields(t *testing.T) {
	reqs, err := parseBatchInput(`[{"resource":"mail","action":"get","params":{"id":9007199254740993}}]`)
	if err != nil {
		t.Fatalf("parseBatchInput() error = %v", err)
	}
	encoded, err := json.Marshal(reqs[0].Params["id"])
	if err != nil {
		t.Fatalf("marshaling parsed id: %v", err)
	}
	if got, want := string(encoded), "9007199254740993"; got != want {
		t.Errorf("marshaled id = %s, want %s", got, want)
	}
	if _, err := planBatchRequests(reqs); err == nil || !strings.Contains(err.Error(), "must be a string") {
		t.Fatalf("planBatchRequests() error = %v, want stable string-id rejection", err)
	}

	if _, err := parseBatchInput(`[{"resource":"mail","action":"get","paramz":{}}]`); err == nil {
		t.Fatal("parseBatchInput() accepted misspelled top-level field paramz")
	}
}

func TestPlanBatchRequests_RejectsWhitespaceOnlyNames(t *testing.T) {
	for name, req := range map[string]batchRequest{
		"resource": {Resource: "  ", Action: "get"},
		"action":   {Resource: "mail", Action: "\t"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := planBatchRequests([]batchRequest{req}); err == nil {
				t.Fatalf("planBatchRequests(%+v) error = nil", req)
			}
		})
	}
}

func TestFilesCreate_RejectsNonRegularInputBeforeDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/null is a Unix non-regular file")
	}
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"files", "create", "--file", "/dev/null", "--dry-run", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want non-regular file rejected; stdout=%s", stdout)
	}
	errCode, _ := decodeStructuredError(t, stderr)
	if errCode != "invalid_argument" {
		t.Errorf("error code = %q, want invalid_argument", errCode)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want validation before network", len(srv.Requests()))
	}
}

func TestImports_RejectNonRegularInputBeforeDryRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/dev/null is a Unix non-regular file")
	}

	tests := map[string][]string{
		"contacts": {"contacts", "import", "--file", "/dev/null", "--dry-run", "--llm"},
		"events":   {"events", "import", "--file", "/dev/null", "--dry-run", "--llm"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code == 0 {
				t.Fatalf("exit code = 0, want non-regular file rejected; stdout=%s", stdout)
			}
			errCode, _ := decodeStructuredError(t, stderr)
			if errCode != "invalid_argument" {
				t.Errorf("error code = %q, want invalid_argument", errCode)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("made %d requests, want validation before network", len(srv.Requests()))
			}
		})
	}
}

func TestEventsList_RejectsMalformedItemsInStructuredMode(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": []any{7}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want malformed event rejected; stdout=%s", stdout)
	}
	errCode, _ := decodeStructuredError(t, stderr)
	if errCode != "invalid_response" {
		t.Errorf("error code = %q, want invalid_response", errCode)
	}
}

func TestEventsList_RejectsNullCollection(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": nil}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	errCode, _ := decodeStructuredError(t, stderr)
	if errCode != "invalid_response" {
		t.Errorf("error code = %q, want invalid_response", errCode)
	}
}

func TestChronosDeleteResultError_RequiresMatchingDeletionEvidence(t *testing.T) {
	tests := map[string][]byte{
		"empty result object":  []byte(`[{}]`),
		"different event":      []byte(`[{"folderId":"cal://0/32","id":"evt-2","deleted":[{"id":"evt-2"}]}]`),
		"empty deleted list":   []byte(`[{"folderId":"cal://0/32","id":"evt-1","deleted":[]}]`),
		"wrong result folder":  []byte(`[{"folderId":"cal://0/99","id":"evt-1","deleted":[{"folder":"cal://0/99","id":"evt-1"}]}]`),
		"wrong deleted folder": []byte(`[{"folderId":"cal://0/32","id":"evt-1","deleted":[{"folder":"cal://0/99","id":"evt-1"}]}]`),
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			err := chronosDeleteResultError(response, "evt-1", "cal://0/32")
			if err == nil || err.Code != "invalid_response" {
				t.Errorf("chronosDeleteResultError(%s) = %v, want invalid_response", response, err)
			}
		})
	}
}

func TestFilesDelete_RejectsMalformedSuccessPayload(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": "36", "title": "file.txt", "filename": "file.txt",
		"file_mimetype": "text/plain", "file_size": 4, "last_modified": 123,
	}}})
	srv.Handle("/ajax/infostore", "delete", testutil.Response{Body: map[string]any{"data": map[string]any{}}})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"files", "delete", "--id", "36/2", "--confirm", "36/2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want malformed delete payload rejected; stdout=%s", stdout)
	}
	errCode, _ := decodeStructuredError(t, stderr)
	if errCode != "invalid_response" {
		t.Errorf("error code = %q, want invalid_response", errCode)
	}
}
