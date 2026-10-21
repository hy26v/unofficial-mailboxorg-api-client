// ABOUTME: CLI integration tests for the attachments resource (OX attachment module):
// ABOUTME: create, list, get/download, delete, multipart shape, and safeguards.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func attachmentsTestServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	return srv
}

func attachmentsEnv() map[string]string {
	return map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
}

// --- list ---

func TestAttachmentsList_LLMMode_Success(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{1, "notes.txt", 42, "text/plain"},
		}},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "list", "--module", "7", "--object-id", "4", "--folder", "33", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"attachments":[{"id":1,"filename":"notes.txt","file_size":42,"file_mimetype":"text/plain"}]}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	found := false
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/attachment" && r.URL.Query().Get("action") == "all" {
			found = true
			if r.URL.Query().Get("module") != "7" || r.URL.Query().Get("attached") != "4" || r.URL.Query().Get("folder") != "33" {
				t.Errorf("query = %v, want module=7 attached=4 folder=33", r.URL.Query())
			}
			if r.URL.Query().Get("columns") != "1,803,804,805" {
				t.Errorf("columns = %q, want 1,803,804,805", r.URL.Query().Get("columns"))
			}
		}
	}
	if !found {
		t.Fatal("no all request observed")
	}
}

func TestAttachmentsList_ZeroByteFileSizeRemainsSelectable(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			[]any{1, "empty.txt", 0, "text/plain"},
		}},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "list", "--module", "7", "--object-id", "4", "--folder", "33",
		"--format", "json", "--select", "file_size",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := "{\"attachments\":[{\"file_size\":0}]}\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestAttachmentsList_MissingRequiredArgs(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{"attachments", "list", "--module", "7", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "--object-id") && !strings.Contains(stderr, "--folder") {
		t.Errorf("stderr = %q, want mention of missing --object-id/--folder", stderr)
	}
}

func TestAttachmentsCommonIDs_RejectInvalidValuesLocally(t *testing.T) {
	tests := []struct {
		name string
		flag string
		args []string
	}{
		{name: "module_text", flag: "--module", args: []string{"attachments", "list", "--module", "abc", "--object-id", "4", "--folder", "33", "--llm"}},
		{name: "module_zero", flag: "--module", args: []string{"attachments", "list", "--module", "0", "--object-id", "4", "--folder", "33", "--llm"}},
		{name: "module_negative", flag: "--module", args: []string{"attachments", "list", "--module", "-1", "--object-id", "4", "--folder", "33", "--llm"}},
		{name: "module_unsupported", flag: "--module", args: []string{"attachments", "list", "--module", "2", "--object-id", "4", "--folder", "33", "--llm"}},
		{name: "object_text", flag: "--object-id", args: []string{"attachments", "list", "--module", "7", "--object-id", "abc", "--folder", "33", "--llm"}},
		{name: "object_zero", flag: "--object-id", args: []string{"attachments", "list", "--module", "7", "--object-id", "0", "--folder", "33", "--llm"}},
		{name: "object_negative", flag: "--object-id", args: []string{"attachments", "list", "--module", "7", "--object-id", "-1", "--folder", "33", "--llm"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := attachmentsTestServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, attachmentsEnv(), tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid %s rejected; stdout=%s", tt.flag, stdout)
			}
			if !strings.Contains(stderr, tt.flag) {
				t.Fatalf("stderr = %q, want mention of %s", stderr, tt.flag)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("invalid %s sent %d network requests", tt.flag, len(srv.Requests()))
			}
		})
	}
}

func TestAttachmentsList_APIError(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "all", testutil.Response{
		Body: map[string]any{"error": "Unknown module", "code": "SVL-0021", "categories": "USER_INPUT"},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "list", "--module", "7", "--object-id", "4", "--folder", "33", "--llm",
	})

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

// --- get ---

func TestAttachmentsGet_LLMMode_Success(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 1, "filename": "notes.txt", "file_size": 42, "file_mimetype": "text/plain"}},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":1,"filename":"notes.txt","file_size":42,"file_mimetype":"text/plain","comment":""}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestAttachmentsGet_EmptyCommentRemainsSelectable(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 1, "filename": "notes.txt", "file_size": 42, "file_mimetype": "text/plain",
		}},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1",
		"--format", "json", "--select", "comment",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if want := "{\"comment\":\"\"}\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestAttachmentsItemIDs_RejectInvalidValuesLocally(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "get_text", args: []string{"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "abc", "--llm"}},
		{name: "get_zero", args: []string{"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "0", "--llm"}},
		{name: "get_negative", args: []string{"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "-1", "--llm"}},
		{name: "delete_text", args: []string{"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "abc", "--confirm", "abc", "--dry-run", "--llm"}},
		{name: "delete_zero", args: []string{"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "0", "--confirm", "0", "--dry-run", "--llm"}},
		{name: "delete_negative", args: []string{"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "-1", "--confirm", "-1", "--dry-run", "--llm"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := attachmentsTestServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, attachmentsEnv(), tt.args)
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid --id rejected; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "--id") {
				t.Fatalf("stderr = %q, want mention of --id", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("invalid --id sent %d network requests", len(srv.Requests()))
			}
		})
	}
}

func TestAttachmentsGet_RejectsMismatchedResponseID(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": 2, "filename": "wrong.txt", "file_size": 42, "file_mimetype": "text/plain",
		}},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAttachmentsGet_RejectsNegativeFileSize(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 1, "filename": "broken.txt", "file_size": -1, "file_mimetype": "text/plain",
	}}})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAttachmentsGet_RejectsMissingFileSize(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 1, "filename": "broken.txt", "file_mimetype": "text/plain",
	}}})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing file size rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAttachmentsGet_RejectsMissingFilename(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 1, "file_size": 4, "file_mimetype": "text/plain",
	}}})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing filename rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAttachmentsGet_RejectsBlankFilename(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 1, "filename": "   ", "file_size": 4, "file_mimetype": "text/plain",
	}}})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want blank filename rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAttachmentsGet_RejectsMissingMIMEType(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 1, "filename": "notes.txt", "file_size": 4,
	}}})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing MIME type rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAttachmentsGet_RejectsBlankMIMEType(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 1, "filename": "notes.txt", "file_size": 4, "file_mimetype": "   ",
	}}})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want blank MIME type rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestAttachmentsGet_SaveTo_DownloadsContent(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": 1, "filename": "notes.txt", "file_size": 5, "file_mimetype": "text/plain"}},
	})
	srv.Handle("/ajax/attachment", "document", testutil.Response{Body: []byte("hello")})

	saveTo := filepath.Join(t.TempDir(), "out.txt")
	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--save-to", saveTo, "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "hello") {
		t.Errorf("stdout must never contain raw file content: %q", stdout)
	}
	if !strings.Contains(stdout, `"saved_to"`) {
		t.Errorf("stdout = %q, want saved_to field", stdout)
	}
	content, err := os.ReadFile(saveTo)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("saved content = %q, want %q", content, "hello")
	}
}

func TestAttachmentsGet_RejectsDownloadSizeMismatchBeforeWriting(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 1, "filename": "notes.txt", "file_size": 5, "file_mimetype": "text/plain",
	}}})
	srv.Handle("/ajax/attachment", "document", testutil.Response{Body: []byte("hey")})

	saveTo := filepath.Join(t.TempDir(), "out.txt")
	if err := os.WriteFile(saveTo, []byte("keep"), 0o600); err != nil {
		t.Fatalf("seeding destination: %v", err)
	}
	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33",
		"--id", "1", "--save-to", saveTo, "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	content, err := os.ReadFile(saveTo)
	if err != nil {
		t.Fatalf("reading destination: %v", err)
	}
	if string(content) != "keep" {
		t.Fatalf("destination = %q, want original content", content)
	}
}

func TestAttachmentsGet_APIError(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "get", testutil.Response{
		Body: map[string]any{"error": "The attachment you requested no longer exists.", "code": "ATT-0405", "categories": "USER_INPUT"},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "get", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "9", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "ATT-0405") {
		t.Errorf("stderr = %q, want ATT-0405 code", stderr)
	}
}

// --- delete safeguards (written before the successful-path test) ---

func TestAttachmentsDelete_MissingConfirm_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--llm",
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
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/attachment" {
			t.Fatalf("unexpected request to /ajax/attachment: %s", r.URL)
		}
	}
}

func TestAttachmentsDelete_ConfirmMismatch_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--confirm", "2", "--llm",
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
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/attachment" {
			t.Fatalf("unexpected request to /ajax/attachment: %s", r.URL)
		}
	}
}

func TestAttachmentsDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--confirm", "1", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/attachment" {
			t.Fatalf("unexpected request to /ajax/attachment: %s", r.URL)
		}
	}
}

func TestAttachmentsDelete_LLMMode_Success(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "detach", testutil.Response{Body: map[string]any{"data": ""}})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--confirm", "1", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":1,"deleted":true}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	found := false
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/attachment" && r.URL.Query().Get("action") == "detach" {
			found = true
			if r.Method != http.MethodPut {
				t.Errorf("detach request method = %q, want PUT", r.Method)
			}
			if r.URL.Query().Get("module") != "7" || r.URL.Query().Get("attached") != "4" || r.URL.Query().Get("folder") != "33" {
				t.Errorf("query = %v, want module=7 attached=4 folder=33", r.URL.Query())
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read detach request body: %v", err)
			}
			if string(body) != `[1]` {
				t.Errorf("detach request body = %q, want numeric attachment-id array [1]", body)
			}
		}
	}
	if !found {
		t.Fatal("no detach request observed")
	}
}

func TestAttachmentsDelete_APIError(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "detach", testutil.Response{
		Body: map[string]any{"error": "Invalid parameter", "code": "SVL-0004", "categories": "USER_INPUT"},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "delete", "--module", "7", "--object-id", "4", "--folder", "33", "--id", "1", "--confirm", "1", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "SVL-0004") {
		t.Errorf("stderr = %q, want SVL-0004 code", stderr)
	}
}

// --- human mode smoke test ---

func TestAttachmentsList_HumanMode_Table(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "all", testutil.Response{
		Body: map[string]any{"data": []any{
			map[string]any{"id": 1, "filename": "notes.txt", "file_size": 42, "file_mimetype": "text/plain"},
		}},
	})

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "list", "--module", "7", "--object-id", "4", "--folder", "33",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "notes.txt") {
		t.Errorf("stdout = %q, want notes.txt in table", stdout)
	}
}

// --- create ---

func TestAttachmentsCreate_LLMMode_UsesIndexedMultipartFields(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "attach", testutil.Response{
		Body: []byte(`<html><script>parent.callback_attach({"data":[7],"timestamp":1785000000123});</script></html>`),
	})

	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("hello attachment"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "create", "--module", "7", "--object-id", "4", "--folder", "33",
		"--file", file, "--comment", "live fixture", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if got, want := stdout, "{\"id\":7}\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}

	var attachRequestFound bool
	for _, req := range srv.Requests() {
		if req.URL.Path != "/ajax/attachment" || req.URL.Query().Get("action") != "attach" {
			continue
		}
		attachRequestFound = true
		if req.Method != http.MethodPost {
			t.Errorf("attach request method = %q, want POST", req.Method)
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if req.FormValue("json") != "" {
			t.Fatalf("legacy json field was sent: %q", req.FormValue("json"))
		}
		var meta map[string]any
		if err := json.Unmarshal([]byte(req.FormValue("json_0")), &meta); err != nil {
			t.Fatalf("json_0 = %q: %v", req.FormValue("json_0"), err)
		}
		if len(meta) != 4 || meta["folder"] != "33" || meta["attached"] != float64(4) || meta["module"] != float64(7) || meta["comment"] != "live fixture" {
			t.Fatalf("json_0 metadata = %#v", meta)
		}
		uploaded, header, err := req.FormFile("file_0")
		if err != nil {
			t.Fatalf("file_0 missing: %v", err)
		}
		content, _ := io.ReadAll(uploaded)
		uploaded.Close()
		if header.Filename != "notes.txt" || string(content) != "hello attachment" {
			t.Fatalf("uploaded filename=%q content=%q", header.Filename, content)
		}
	}
	if !attachRequestFound {
		t.Fatal("no attachment?action=attach request observed")
	}
}

func TestAttachmentsCreate_EmptyServerResultIsInvalidResponse(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()
	srv.Handle("/ajax/attachment", "attach", testutil.Response{
		Body: []byte(`<script>parent.callback_attach({"data":[],"timestamp":0});</script>`),
	})

	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "create", "--module", "7", "--object-id", "4", "--folder", "33",
		"--file", file, "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" || !strings.Contains(stderr, "no attachment id") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestAttachmentsCreate_MultipleServerIDsAreInvalidResponse(t *testing.T) {
	if id, err := decodeCreatedAttachmentID([]byte(`[7,8]`)); err == nil {
		t.Fatalf("decodeCreatedAttachmentID([7,8]) = %d, nil; want ambiguous response rejected", id)
	}
}

func TestAttachmentsCreate_DryRun_NoNetworkCall(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()

	file := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "create", "--module", "7", "--object-id", "4", "--folder", "33",
		"--file", file, "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Fatalf("stdout = %q, want dry_run", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/attachment" {
			t.Fatalf("dry-run contacted attachment endpoint: %s", req.URL)
		}
	}
}

func TestAttachmentsCreate_MissingFileIsInvalidArgument(t *testing.T) {
	srv := attachmentsTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, attachmentsEnv(), []string{
		"attachments", "create", "--module", "7", "--object-id", "4", "--folder", "33", "--llm",
	})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" || !strings.Contains(stderr, "--file") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}
