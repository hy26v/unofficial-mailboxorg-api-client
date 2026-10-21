// ABOUTME: CLI integration tests for contacts export/import: vCard text passthrough,
// ABOUTME: --output file writing, multipart upload, and per-item error surfacing.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

const testVCard = "BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Test Contact\r\nEND:VCARD\r\n"

func assertImportMultipartRequest(t *testing.T, srv *testutil.Server, action, folder, filename, contentType, content string) {
	t.Helper()
	var importRequest *http.Request
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/import" && req.URL.Query().Get("action") == action {
			importRequest = req
			break
		}
	}
	if importRequest == nil {
		t.Fatalf("no /ajax/import?action=%s request observed", action)
	}
	if importRequest.Method != http.MethodPost {
		t.Errorf("import request method = %q, want POST", importRequest.Method)
	}
	if importRequest.URL.Query().Get("folder") != folder {
		t.Errorf("import folder = %q, want %q", importRequest.URL.Query().Get("folder"), folder)
	}
	if err := importRequest.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("parse import multipart form: %v", err)
	}
	if len(importRequest.MultipartForm.Value) != 1 || len(importRequest.MultipartForm.Value["json"]) != 1 {
		t.Fatalf("import multipart value fields = %#v, want one json field", importRequest.MultipartForm.Value)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(importRequest.MultipartForm.Value["json"][0]), &metadata); err != nil {
		t.Fatalf("decode import json metadata: %v", err)
	}
	if len(metadata) != 0 {
		t.Errorf("import json metadata has %d fields, want empty object", len(metadata))
	}
	if len(importRequest.MultipartForm.File) != 1 || len(importRequest.MultipartForm.File["file"]) != 1 {
		t.Fatalf("import multipart file fields = %#v, want one file part", importRequest.MultipartForm.File)
	}
	uploaded, header, err := importRequest.FormFile("file")
	if err != nil {
		t.Fatalf("open import file part: %v", err)
	}
	defer uploaded.Close()
	uploadedContent, err := io.ReadAll(uploaded)
	if err != nil {
		t.Fatalf("read import file part: %v", err)
	}
	if header.Filename != filename {
		t.Errorf("import filename = %q, want %q", header.Filename, filename)
	}
	if header.Header.Get("Content-Type") != contentType {
		t.Errorf("import content type = %q, want %q", header.Header.Get("Content-Type"), contentType)
	}
	if string(uploadedContent) != content {
		t.Errorf("imported file bytes differed from source")
	}
}

func TestContactsExport_HumanMode_WritesRawVCardToStdout(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/export", "VCARD", testutil.Response{Body: []byte(testVCard)})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "export", "--folder", "33"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != testVCard {
		t.Errorf("stdout = %q, want raw vCard %q", stdout, testVCard)
	}
}

func TestContactsExport_DefaultFolder_WhenOmitted(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/export", "VCARD", testutil.Response{Body: []byte(testVCard)})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	_, stderr, code := run(t, srv, env, []string{"contacts", "export"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	var found bool
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/export" {
			found = true
			if r.URL.Query().Get("folder") != "33" {
				t.Errorf("folder = %q, want 33 (from user.get folder_id)", r.URL.Query().Get("folder"))
			}
		}
	}
	if !found {
		t.Fatal("no request to /ajax/export observed")
	}
}

func TestContactsExport_Output_WritesFileAndPrintsConfirmation(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/export", "VCARD", testutil.Response{Body: []byte(testVCard)})

	dst := filepath.Join(t.TempDir(), "contacts.vcf")
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "export", "--folder", "33", "--output", dst, "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("could not read --output file: %v", err)
	}
	if string(content) != testVCard {
		t.Errorf("file content = %q, want %q", content, testVCard)
	}
	if !strings.Contains(stdout, `"saved_to":`) || !strings.Contains(stdout, dst) {
		t.Errorf("stdout = %q, want saved_to mentioning %s", stdout, dst)
	}
	if strings.Contains(stdout, "BEGIN:VCARD") {
		t.Errorf("stdout = %q, want no raw vCard content when --output is used", stdout)
	}
}

func TestContactsImport_Success(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "VCARD", testutil.Response{
		Body: map[string]any{"data": []map[string]any{{"id": "9", "folder_id": "33", "last_modified": 1784857952353}}},
	})

	src := filepath.Join(t.TempDir(), "in.vcf")
	if err := os.WriteFile(src, []byte(testVCard), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "import", "--file", src, "--folder", "33", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"9","folder_id":"33","items":[{"index":0,"id":"9","folder_id":"33"}],"count":1,"failed":[],"failed_count":0}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	assertImportMultipartRequest(t, srv, "VCARD", "33", "in.vcf", "text/vcard", testVCard)
}

func TestContactsImport_RejectsIncompleteSuccessResult(t *testing.T) {
	tests := []struct {
		name   string
		result map[string]any
	}{
		{name: "missing id", result: map[string]any{"folder_id": "33"}},
		{name: "missing folder id", result: map[string]any{"id": "9"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := contactsCRUDTestServer()
			defer srv.Close()
			srv.Handle("/ajax/import", "VCARD", testutil.Response{
				Body: map[string]any{"data": []map[string]any{tt.result}},
			})

			src := filepath.Join(t.TempDir(), "in.vcf")
			if err := os.WriteFile(src, []byte(testVCard), 0o600); err != nil {
				t.Fatal(err)
			}

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{
				"contacts", "import", "--file", src, "--folder", "33", "--llm",
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

func TestContactsImport_RejectsMismatchedResultFolder(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "VCARD", testutil.Response{
		Body: map[string]any{"data": []map[string]any{{"id": "9", "folder_id": "34", "last_modified": 1784857952353}}},
	})

	src := filepath.Join(t.TempDir(), "in.vcf")
	if err := os.WriteFile(src, []byte(testVCard), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "import", "--file", src, "--folder", "33", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched result folder rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestContactsImport_PerItemError_ReportedOnStdoutWithNonzeroExit(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "VCARD", testutil.Response{
		Body: map[string]any{"data": []map[string]any{{"error": "Validation failed", "code": "VCARD-0003"}}},
	})

	src := filepath.Join(t.TempDir(), "in.vcf")
	if err := os.WriteFile(src, []byte(testVCard), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "import", "--file", src, "--folder", "33", "--llm"})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := `{"id":null,"folder_id":null,"items":[],"count":0,"failed":[{"index":0,"code":"VCARD-0003","message":"Validation failed"}],"failed_count":1}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "import_incomplete") || !strings.Contains(stderr, "Validation failed") {
		t.Errorf("stderr = %q, want import_incomplete with the validation error message", stderr)
	}
}

func TestContactsImport_MissingFile_InvalidArgumentError(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "import", "--llm"})

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

func TestContactsImport_DryRun_NoNetworkCall(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	src := filepath.Join(t.TempDir(), "in.vcf")
	if err := os.WriteFile(src, []byte(testVCard), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "import", "--file", src, "--folder", "33", "--dry-run", "--llm"})

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

// TestContactsExport_StructuredFormatWithoutOutput_InvalidArgument mirrors the events
// export contract: a vCard document is not structured output, so structured callers must
// route it to a file.
func TestContactsExport_StructuredFormatWithoutOutput_InvalidArgument(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/export", "VCARD", testutil.Response{Body: []byte("BEGIN:VCARD\r\nEND:VCARD\r\n")})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "export", "--folder", "123", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if strings.Contains(stdout, "BEGIN:VCARD") {
		t.Errorf("stdout = %q, want no raw vCard in structured mode", stdout)
	}
	if !strings.Contains(stderr, "--output") {
		t.Errorf("stderr = %q, want the caller pointed at --output", stderr)
	}
}

// TestContactsExport_HumanModeWithoutOutput_StreamsVCard pins that the human path still
// streams the document to stdout.
func TestContactsExport_HumanModeWithoutOutput_StreamsVCard(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/export", "VCARD", testutil.Response{Body: []byte("BEGIN:VCARD\r\nEND:VCARD\r\n")})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "export", "--folder", "123"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "BEGIN:VCARD") {
		t.Errorf("stdout = %q, want the vCard document", stdout)
	}
}

// TestNewImportOutput_ReportsEveryEntry covers the per-object contract of an import
// response: the server returns one result per object in the file, so a rejection can appear
// at any index, and every stored object must stay in the report.
func TestNewImportOutput_ReportsEveryEntry(t *testing.T) {
	ok1 := importResult{ID: "1", FolderID: "33"}
	ok2 := importResult{ID: "2", FolderID: "33"}
	bad := importResult{Error: "Bad vCard: unexpected token in line 9", Code: "VCARD-0003"}
	noID := importResult{FolderID: "33"}
	elsewhere := importResult{ID: "3", FolderID: "34"}
	storedWithError := importResult{ID: "4", FolderID: "33", Error: "Truncated value in field 'note'", Code: "CON-0101"}

	for _, tc := range []struct {
		name       string
		results    []importResult
		wantStored []string
		wantFailed []int
	}{
		{"all succeeded", []importResult{ok1, ok2}, []string{"1", "2"}, nil},
		{"first rejected", []importResult{bad, ok1}, []string{"1"}, []int{0}},
		{"last rejected", []importResult{ok1, ok2, bad}, []string{"1", "2"}, []int{2}},
		{"only rejection", []importResult{bad}, nil, []int{0}},
		{"result without id", []importResult{ok1, noID}, []string{"1"}, []int{1}},
		{"stored in another folder", []importResult{elsewhere}, []string{"3"}, []int{0}},
		{"stored with an error", []importResult{ok1, storedWithError}, []string{"1", "4"}, []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := newImportOutput(tc.results, "33", "contacts")
			var stored []string
			for _, item := range out.Items {
				stored = append(stored, item.ID)
			}
			var failed []int
			for _, f := range out.Failed {
				failed = append(failed, f.Index)
			}
			if !reflect.DeepEqual(stored, tc.wantStored) || !reflect.DeepEqual(failed, tc.wantFailed) {
				t.Fatalf("stored = %v, failed = %v; want %v and %v", stored, failed, tc.wantStored, tc.wantFailed)
			}
			if out.Count != len(out.Items) || out.FailedCount != len(out.Failed) {
				t.Errorf("count = %d, failed_count = %d, want list lengths", out.Count, out.FailedCount)
			}
			if (importIncompleteError(out, len(tc.results), "contacts") == nil) != (len(tc.wantFailed) == 0) {
				t.Errorf("importIncompleteError does not match failed list %v", tc.wantFailed)
			}
		})
	}
}

// TestContactsImport_RejectionAfterTheFirstEntry_ReportsStoredAndFailed is the regression
// test for orphaned imports (audit F-03): when a later entry was rejected, the ids of the
// entries already stored were discarded, so the caller could not verify or remove them.
func TestContactsImport_RejectionAfterTheFirstEntry_ReportsStoredAndFailed(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "VCARD", testutil.Response{
		Body: map[string]any{"data": []map[string]any{
			{"id": "9", "folder_id": "33", "last_modified": 1784857952353},
			{"error": "Bad vCard: unexpected token in line 9", "code": "VCARD-0003"},
		}},
	})

	src := filepath.Join(t.TempDir(), "in.vcf")
	if err := os.WriteFile(src, []byte(testVCard), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "import", "--file", src, "--folder", "33", "--llm"})

	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout=%q", code, stdout)
	}
	want := `{"id":"9","folder_id":"33","items":[{"index":0,"id":"9","folder_id":"33"}],"count":1,` +
		`"failed":[{"index":1,"code":"VCARD-0003","message":"Bad vCard: unexpected token in line 9"}],"failed_count":1}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if !strings.Contains(stderr, "import_incomplete") || !strings.Contains(stderr, "unexpected token in line 9") {
		t.Errorf("stderr = %q, want import_incomplete with the rejected entry's message", stderr)
	}

	// Human output carries the same report.
	stdout, _, code = run(t, srv, env, []string{"contacts", "import", "--file", src, "--folder", "33"})
	if code != 1 || !strings.Contains(stdout, `"id":"9"`) || !strings.Contains(stdout, "failed_count") {
		t.Errorf("human exit = %d, stdout = %q, want the stored id and failed list", code, stdout)
	}
}

// TestContactsImport_EveryEntryAccepted_Succeeds pins the other side: a multi-contact file
// the server accepted in full is still a success.
func TestContactsImport_EveryEntryAccepted_Succeeds(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()
	srv.Handle("/ajax/import", "VCARD", testutil.Response{
		Body: map[string]any{"data": []map[string]any{
			{"id": "9", "folder_id": "33", "last_modified": 1784857952353},
			{"id": "10", "folder_id": "33", "last_modified": 1784857952354},
		}},
	})

	src := filepath.Join(t.TempDir(), "in.vcf")
	if err := os.WriteFile(src, []byte(testVCard), 0o600); err != nil {
		t.Fatal(err)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{"contacts", "import", "--file", src, "--folder", "33", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"9","folder_id":"33","items":[{"index":0,"id":"9","folder_id":"33"},{"index":1,"id":"10","folder_id":"33"}],"count":2,"failed":[],"failed_count":0}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want every imported contact identity", stdout)
	}
}
