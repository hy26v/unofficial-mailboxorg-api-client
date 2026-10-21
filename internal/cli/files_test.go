// ABOUTME: CLI integration tests for the files resource (OX infostore module): list, get
// ABOUTME: (with content download), create (upload), update, delete, and its safeguards.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func filesTestServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/config/folder/infostore", "", testutil.Response{Body: map[string]any{"data": 36, "timestamp": 1}})
	return srv
}

func filesEnv() map[string]string {
	return map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
}

// --- delete safeguards (written first: the destructive operation must refuse locally,
// without a network call, before the successful-path test exists) ---

func TestFilesDelete_MissingConfirm_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "delete", "--id", "36/2", "--llm"})

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
		if r.URL.Path == "/ajax/infostore" {
			t.Fatalf("unexpected request to /ajax/infostore: %s", r.URL)
		}
	}
}

func TestFilesDelete_ConfirmMismatch_RefusesLocallyNoNetworkCall(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "delete", "--id", "36/2", "--confirm", "36/3", "--llm"})

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
		if r.URL.Path == "/ajax/infostore" {
			t.Fatalf("unexpected request to /ajax/infostore: %s", r.URL)
		}
	}
}

func TestFilesDelete_DryRun_NoNetworkCall(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "delete", "--id", "36/2", "--confirm", "36/2", "--dry-run", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/infostore" {
			t.Fatalf("unexpected request to /ajax/infostore: %s", r.URL)
		}
	}
}

func TestFilesDelete_LLMMode_Success(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "36/2", "folder_id": "36", "last_modified": 1784862919914}},
	})
	srv.Handle("/ajax/infostore", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "delete", "--id", "36/2", "--confirm", "36/2", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"36/2","deleted":true}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	foundGet := false
	foundDelete := false
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/infostore" && r.URL.Query().Get("action") == "get" {
			foundGet = true
			if r.Method != http.MethodGet {
				t.Errorf("get method = %q, want GET", r.Method)
			}
			if r.URL.Query().Get("id") != "36/2" {
				t.Errorf("get id = %q, want 36/2", r.URL.Query().Get("id"))
			}
		}
		if r.URL.Path == "/ajax/infostore" && r.URL.Query().Get("action") == "delete" {
			foundDelete = true
			if r.Method != http.MethodPut {
				t.Errorf("delete method = %q, want PUT", r.Method)
			}
			if r.URL.Query().Get("timestamp") != "1784862919914" {
				t.Errorf("timestamp = %q, want 1784862919914", r.URL.Query().Get("timestamp"))
			}
			var got []map[string]string
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Fatalf("decoding delete request body: %v", err)
			}
			wantBody := []map[string]string{{"id": "36/2", "folder": "36"}}
			if !reflect.DeepEqual(got, wantBody) {
				t.Errorf("delete request body = %#v, want %#v", got, wantBody)
			}
		}
	}
	if !foundGet || !foundDelete {
		t.Fatalf("observed get=%v delete=%v, want both requests", foundGet, foundDelete)
	}
}

func TestFilesDelete_ServerRefusedTheDelete_IsAnError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "36/2", "folder_id": "36", "last_modified": 1784862919914}},
	})
	srv.Handle("/ajax/infostore", "delete", testutil.Response{
		Body: map[string]any{"data": []any{map[string]any{"id": "36/2", "folder": "36"}}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "delete", "--id", "36/2", "--confirm", "36/2", "--llm"})

	if code == 0 {
		t.Fatalf("exit code = 0, want refused delete rejected; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "api_error") || !strings.Contains(stderr, "rejected") {
		t.Errorf("stderr = %q, want structured rejected-delete error", stderr)
	}
}

func TestFilesDelete_APIError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"error": "The file you requested does not exist.", "code": "FILE_STORAGE-0026", "categories": "USER_INPUT"},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "delete", "--id", "36/9", "--confirm", "36/9", "--llm"})

	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "FILE_STORAGE-0026") {
		t.Errorf("stderr = %q, want FILE_STORAGE-0026 code", stderr)
	}
}

// --- list ---

func TestFilesList_LLMMode_CompactJSON(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "all", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"36/2", "36", "report.pdf", "report.pdf", "application/pdf", 1024},
		}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--llm"})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"files":[{"id":"36/2","folder_id":"36","title":"report.pdf","filename":"report.pdf","mimetype":"application/pdf","size":1024}],"truncated":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	req := srv.Requests()[len(srv.Requests())-1]
	if req.URL.Query().Get("folder") != "36" {
		t.Errorf("folder = %q, want the resolved default %q", req.URL.Query().Get("folder"), "36")
	}
}

func TestFilesList_RejectsNullCollection(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "all", testutil.Response{Body: map[string]any{"data": nil}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesList_FolderFlag_UsesGivenFolder(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "all", testutil.Response{Body: map[string]any{"data": [][]any{}}})

	_, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--folder", "99"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}

	req := srv.Requests()[len(srv.Requests())-1]
	if req.URL.Query().Get("folder") != "99" {
		t.Errorf("folder = %q, want 99", req.URL.Query().Get("folder"))
	}
}

func TestFilesList_RejectsMismatchedResponseFolder(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "all", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"37/2", "37", "wrong-folder.pdf", "wrong-folder.pdf", "application/pdf", 1024},
		}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--folder", "36", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched response folder rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesList_LimitFlag_TruncatesClientSide(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "all", testutil.Response{
		Body: map[string]any{"data": [][]any{
			{"36/1", "36", "a.txt", "a.txt", "text/plain", 1},
			{"36/2", "36", "b.txt", "b.txt", "text/plain", 2},
		}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--limit", "1", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"truncated":true`) {
		t.Errorf("stdout = %q, want truncated true", stdout)
	}
	if strings.Count(stdout, `"id":`) != 1 {
		t.Errorf("stdout = %q, want exactly 1 item", stdout)
	}
}

func TestFilesList_UnknownFlag_InvalidArgumentError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--bogus"})
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

// --- get ---

func TestFilesGet_HumanMode_Success(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "36/2", "folder_id": "36", "title": "report.pdf", "filename": "report.pdf",
			"file_mimetype": "application/pdf", "file_size": 1024, "description": "", "last_modified": 1784862919914,
		}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "report.pdf") {
		t.Errorf("stdout = %q, want report.pdf", stdout)
	}
}

func TestFilesGet_LLMMode_CompactJSON(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "36/2", "folder_id": "36", "title": "report.pdf", "filename": "report.pdf",
			"file_mimetype": "application/pdf", "file_size": 1024, "last_modified": 1784862919914,
		}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"36/2","folder_id":"36","title":"report.pdf","filename":"report.pdf","mimetype":"application/pdf","size":1024,"description":"","last_modified":1784862919914}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestFilesGet_EmptyDescriptionRemainsSelectable(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "36/2", "folder_id": "36", "title": "report.pdf", "filename": "report.pdf",
			"file_mimetype": "application/pdf", "file_size": 1024, "description": "", "last_modified": 1784862919914,
		}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{
		"files", "get", "--id", "36/2", "--format", "json", "--select", "description",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"description":""}` + "\n"
	if stdout != want {
		t.Fatalf("stdout = %q, want %q", stdout, want)
	}
}

func TestFilesGet_RejectsMismatchedMetadataID(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "36/9", "folder_id": "36", "title": "wrong.pdf", "filename": "wrong.pdf",
			"file_mimetype": "application/pdf", "file_size": 1024, "last_modified": 1784862919914,
		}},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{
		"files", "get", "--id", "36/2", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want failure; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesGet_RejectsNegativeMetadataSize(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": "36", "title": "broken.txt", "filename": "broken.txt",
		"file_mimetype": "text/plain", "file_size": -1, "last_modified": 123,
	}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesGet_RejectsMissingMetadataSize(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": "36", "title": "broken.txt", "filename": "broken.txt",
		"file_mimetype": "text/plain", "last_modified": 123,
	}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing metadata size rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesGet_RejectsMissingMetadataFolder(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": " ", "title": "orphan.txt", "filename": "orphan.txt",
		"file_mimetype": "text/plain", "file_size": 4, "last_modified": 123,
	}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesGet_RejectsMissingMetadataTitle(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": "36", "filename": "report.pdf",
		"file_mimetype": "application/pdf", "file_size": 4, "last_modified": 123,
	}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing metadata title rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesGet_RejectsMissingMetadataFilename(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": "36", "title": "Report",
		"file_mimetype": "application/pdf", "file_size": 4, "last_modified": 123,
	}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing metadata filename rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesGet_RejectsMissingMetadataMIMEType(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": "36", "title": "Report", "filename": "report.pdf",
		"file_size": 4, "last_modified": 123,
	}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want missing metadata MIME type rejected; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesUpdate_RejectsNonPositiveMetadataTimestamp(t *testing.T) {
	for _, timestamp := range []int64{0, -1} {
		t.Run(strconv.FormatInt(timestamp, 10), func(t *testing.T) {
			srv := filesTestServer()
			defer srv.Close()
			srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
				"id": "36/2", "folder_id": "36", "title": "file.txt", "filename": "file.txt",
				"file_mimetype": "text/plain", "file_size": 4, "last_modified": timestamp,
			}}})

			stdout, stderr, code := run(t, srv, filesEnv(), []string{
				"files", "update", "--id", "36/2", "--title", "renamed", "--llm",
			})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
			for _, req := range srv.Requests() {
				if req.URL.Query().Get("action") == "update" {
					t.Fatalf("sent update with invalid timestamp: %s", req.URL)
				}
			}
		})
	}
}

func TestFilesGet_SaveTo_WritesContentNotToStdout(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{
			"id": "36/2", "folder_id": "36", "title": "report.pdf", "filename": "report.pdf",
			"file_mimetype": "application/pdf", "file_size": 5, "last_modified": 1784862919914,
		}},
	})
	srv.Handle("/ajax/infostore", "document", testutil.Response{Body: []byte("hello")})

	savePath := filepath.Join(t.TempDir(), "downloaded.pdf")
	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/2", "--save-to", savePath, "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if strings.Contains(stdout, "hello") {
		t.Errorf("stdout = %q, must not contain raw file content", stdout)
	}
	if !strings.Contains(stdout, `"saved_to":`) || !strings.Contains(stdout, `"bytes_written":5`) {
		t.Errorf("stdout = %q, want saved_to and bytes_written", stdout)
	}
	content, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("saved file content = %q, want hello", content)
	}
}

func TestFilesGet_RejectsDownloadSizeMismatchBeforeWriting(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "36/2", "folder_id": "36", "title": "report.txt", "filename": "report.txt",
		"file_mimetype": "text/plain", "file_size": 5, "last_modified": 123,
	}}})
	srv.Handle("/ajax/infostore", "document", testutil.Response{Body: []byte("hey")})

	savePath := filepath.Join(t.TempDir(), "downloaded.txt")
	if err := os.WriteFile(savePath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("seeding destination: %v", err)
	}
	stdout, stderr, code := run(t, srv, filesEnv(), []string{
		"files", "get", "--id", "36/2", "--save-to", savePath, "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
	content, err := os.ReadFile(savePath)
	if err != nil {
		t.Fatalf("reading destination: %v", err)
	}
	if string(content) != "keep" {
		t.Fatalf("destination = %q, want original content", content)
	}
}

func TestFilesGet_MissingID_InvalidArgumentError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--llm"})
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

func TestFilesGet_NotFound_APIError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"error": "The file you requested does not exist.", "code": "FILE_STORAGE-0026", "categories": "USER_INPUT"},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "get", "--id", "36/9", "--llm"})
	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "FILE_STORAGE-0026") {
		t.Errorf("stderr = %q, want FILE_STORAGE-0026 code", stderr)
	}
}

// --- create ---

func TestFilesCreate_DryRun_NoNetworkCall(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	srcPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(srcPath, []byte("hello upload"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "create", "--file", srcPath, "--dry-run", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/infostore" {
			t.Fatalf("unexpected request to /ajax/infostore: %s", r.URL)
		}
	}
}

func TestFilesCreate_MissingFile_InvalidArgumentError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "create", "--llm"})
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

func TestFilesCreate_NonexistentFile_InvalidArgumentError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "create", "--file", "/nonexistent/path/does-not-exist.txt", "--llm"})
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

func TestFilesCreate_LLMMode_Success(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "new", testutil.Response{
		Body: []byte(`<!DOCTYPE html><html><head><script>(parent["callback_new"])({"data":"36/2","timestamp":1784855719914})</script></head></html>`),
	})

	srcPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(srcPath, []byte("hello upload"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, srv, filesEnv(), []string{
		"files", "create", "--file", srcPath, "--folder", "36",
		"--title", "Live report", "--description", "fixture description", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"36/2","folder_id":"36","title":"Live report"}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	var req *http.Request
	for _, candidate := range srv.Requests() {
		if candidate.URL.Path == "/ajax/infostore" && candidate.URL.Query().Get("action") == "new" {
			req = candidate
		}
	}
	if req == nil {
		t.Fatal("no create request observed")
	}
	if req.Method != http.MethodPost {
		t.Errorf("create method = %q, want POST", req.Method)
	}
	reader, err := req.MultipartReader()
	if err != nil {
		t.Fatalf("opening multipart request: %v", err)
	}
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("reading multipart request: %v", err)
		}
		name := part.FormName()
		if seen[name] {
			t.Fatalf("duplicate multipart part %q", name)
		}
		seen[name] = true
		switch name {
		case "json":
			if part.FileName() != "" {
				t.Errorf("json filename = %q, want empty", part.FileName())
			}
			if part.Header.Get("Content-Type") != "application/json" {
				t.Errorf("json content type = %q, want application/json", part.Header.Get("Content-Type"))
			}
			var got map[string]any
			if err := json.NewDecoder(part).Decode(&got); err != nil {
				t.Fatalf("decoding multipart metadata: %v", err)
			}
			wantMeta := map[string]any{
				"folder_id": "36", "title": "Live report", "filename": "upload.txt",
				"description": "fixture description",
			}
			if !reflect.DeepEqual(got, wantMeta) {
				t.Errorf("multipart metadata = %#v, want %#v", got, wantMeta)
			}
		case "file":
			if part.FileName() != "upload.txt" {
				t.Errorf("file part filename = %q, want upload.txt", part.FileName())
			}
			if !strings.HasPrefix(part.Header.Get("Content-Type"), "text/plain") {
				t.Errorf("file content type = %q, want text/plain", part.Header.Get("Content-Type"))
			}
			got, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("reading uploaded bytes: %v", err)
			}
			if string(got) != "hello upload" {
				t.Errorf("uploaded bytes = %q, want %q", got, "hello upload")
			}
		default:
			t.Fatalf("unexpected multipart part %q", name)
		}
	}
	if !reflect.DeepEqual(seen, map[string]bool{"json": true, "file": true}) {
		t.Errorf("multipart parts = %v, want exactly json and file", seen)
	}
}

func TestFilesCreate_APIError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "new", testutil.Response{
		Body: []byte(`{"error":"no space left","code":"IFO-0100","categories":"USER_INPUT"}`),
	})

	srcPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(srcPath, []byte("hello upload"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "create", "--file", srcPath, "--llm"})
	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "IFO-0100") {
		t.Errorf("stderr = %q, want IFO-0100 code", stderr)
	}
}

// --- update ---

func TestFilesUpdate_DryRun_NoNetworkCall(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "update", "--id", "36/2", "--title", "new title", "--dry-run", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"dry_run":true`) {
		t.Errorf("stdout = %q, want dry_run true", stdout)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/infostore" {
			t.Fatalf("unexpected request to /ajax/infostore: %s", r.URL)
		}
	}
}

func TestFilesUpdate_NoFieldsGiven_InvalidArgumentError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "update", "--id", "36/2", "--llm"})
	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "required") {
		t.Errorf("stderr = %q, want mention of required field", stderr)
	}
}

func TestFilesUpdate_LLMMode_Success(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "36/2", "folder_id": "36", "last_modified": 1784862919914}},
	})
	srv.Handle("/ajax/infostore", "update", testutil.Response{
		Body: map[string]any{"data": "36/2", "timestamp": 1784855746144},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{
		"files", "update", "--id", "36/2", "--title", "Updated report",
		"--description", "updated", "--filename", "updated.txt", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"id":"36/2","updated":true,"timestamp":1784855746144}` + "\n"
	if stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}

	foundGet := false
	foundUpdate := false
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/infostore" && r.URL.Query().Get("action") == "get" {
			foundGet = true
			if r.Method != http.MethodGet || r.URL.Query().Get("id") != "36/2" {
				t.Errorf("preflight request = %s %s, want GET id=36/2", r.Method, r.URL)
			}
		}
		if r.URL.Path == "/ajax/infostore" && r.URL.Query().Get("action") == "update" {
			foundUpdate = true
			if r.Method != http.MethodPut {
				t.Errorf("update method = %q, want PUT", r.Method)
			}
			if r.URL.Query().Get("timestamp") != "1784862919914" {
				t.Errorf("timestamp = %q, want 1784862919914", r.URL.Query().Get("timestamp"))
			}
			if r.URL.Query().Get("id") != "36/2" {
				t.Errorf("id = %q, want 36/2", r.URL.Query().Get("id"))
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("reading request body: %v", err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("decoding request body: %v", err)
			}
			wantBody := map[string]any{
				"title": "Updated report", "description": "updated", "filename": "updated.txt",
			}
			if !reflect.DeepEqual(decoded, wantBody) {
				t.Errorf("update request body = %#v, want %#v", decoded, wantBody)
			}
		}
	}
	if !foundGet || !foundUpdate {
		t.Fatalf("observed get=%v update=%v, want both requests", foundGet, foundUpdate)
	}
}

func TestFilesUpdate_RejectsMismatchedResponseID(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"data": map[string]any{"id": "36/2", "folder_id": "36", "last_modified": 1784862919914}},
	})
	srv.Handle("/ajax/infostore", "update", testutil.Response{
		Body: map[string]any{"data": "36/3", "timestamp": 1784855746144},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "update", "--id", "36/2", "--description", "updated", "--llm"})
	if code == 0 {
		t.Fatalf("exit code = 0, want mismatched response rejected; stdout=%s", stdout)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Errorf("stderr = %q, want invalid_response", stderr)
	}
}

func TestFilesUpdate_APIError(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()
	srv.Handle("/ajax/infostore", "get", testutil.Response{
		Body: map[string]any{"error": "The file you requested does not exist.", "code": "FILE_STORAGE-0026", "categories": "USER_INPUT"},
	})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "update", "--id", "36/9", "--title", "x", "--llm"})
	if code == 0 {
		t.Fatal("expected nonzero exit code")
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(stderr, "FILE_STORAGE-0026") {
		t.Errorf("stderr = %q, want FILE_STORAGE-0026 code", stderr)
	}
}

// --- discovery ---

func TestDescribe_LLM_FilesDelete(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"describe", "files.delete", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"name":"files.delete"`) {
		t.Errorf("stdout = %q, want name field", stdout)
	}
	if !strings.Contains(stdout, `"effect":"destructive"`) {
		t.Errorf("stdout = %q, want destructive effect", stdout)
	}
}

func TestCommands_LLM_ListsFilesOperations(t *testing.T) {
	srv := filesTestServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{"commands", "files", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	for _, name := range []string{"files.list", "files.get", "files.create", "files.update", "files.delete"} {
		if !strings.Contains(stdout, `"name":"`+name+`"`) {
			t.Errorf("stdout missing %s", name)
		}
	}
}

// --- default folder ---

func defaultFolderServer(folder any) *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/config/folder/infostore", "", testutil.Response{Body: map[string]any{"data": folder, "timestamp": 1}})
	return srv
}

func requestsTo(srv *testutil.Server, path string) int {
	count := 0
	for _, req := range srv.Requests() {
		if req.URL.Path == path {
			count++
		}
	}
	return count
}

func TestFilesList_ResolvesDefaultFolderAtRunTime(t *testing.T) {
	srv := defaultFolderServer("77")
	defer srv.Close()
	srv.Handle("/ajax/infostore", "all", testutil.Response{Body: map[string]any{"data": [][]any{
		{"77/2", "77", "report.pdf", "report.pdf", "application/pdf", 1024},
	}}})

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"folder_id":"77"`) {
		t.Errorf("stdout = %q, want files of folder 77", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/config/folder/infostore" && req.Method != http.MethodGet {
			t.Errorf("config request method = %s, want GET", req.Method)
		}
		if req.URL.Path == "/ajax/infostore" && req.URL.Query().Get("folder") != "77" {
			t.Errorf("infostore all folder = %q, want the resolved 77, not a hard-coded id", req.URL.Query().Get("folder"))
		}
	}
}

func TestFilesList_ExplicitFolderSkipsResolution(t *testing.T) {
	srv := defaultFolderServer("77")
	defer srv.Close()
	srv.Handle("/ajax/infostore", "all", testutil.Response{Body: map[string]any{"data": [][]any{}}})

	_, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--folder", "40", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if n := requestsTo(srv, "/ajax/config/folder/infostore"); n != 0 {
		t.Errorf("config requests = %d, want none with --folder", n)
	}
}

func TestFilesList_UnresolvableDefaultFolderIsAClearError(t *testing.T) {
	for name, folder := range map[string]any{"null": nil, "empty": "", "object": map[string]any{"id": 1}} {
		t.Run(name, func(t *testing.T) {
			srv := defaultFolderServer(folder)
			defer srv.Close()
			srv.Handle("/ajax/infostore", "all", testutil.Response{Body: map[string]any{"data": [][]any{}}})

			stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "list", "--llm"})
			if code == 0 || stdout != "" || !strings.Contains(stderr, "pass --folder") {
				t.Fatalf("code=%d stdout=%q stderr=%q, want an error that suggests --folder", code, stdout, stderr)
			}
			if n := requestsTo(srv, "/ajax/infostore"); n != 0 {
				t.Errorf("infostore requests = %d, want none", n)
			}
		})
	}
}

func TestFilesCreate_DryRunWithoutFolderSendsNothing(t *testing.T) {
	srv := defaultFolderServer("77")
	defer srv.Close()
	srcPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "create", "--file", srcPath, "--dry-run", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"folder_id":null`) {
		t.Fatalf("code=%d stdout=%q stderr=%q, want folder_id null", code, stdout, stderr)
	}
	stdout, _, code = run(t, srv, filesEnv(), []string{"files", "create", "--file", srcPath, "--dry-run"})
	if code != 0 || !strings.Contains(stdout, "default folder (resolved at run time)") || strings.Contains(stdout, "36") {
		t.Fatalf("code=%d stdout=%q, want the default folder described, not a number", code, stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry runs sent %d requests", len(srv.Requests()))
	}
}

func TestFilesCreate_UploadsIntoResolvedDefaultFolder(t *testing.T) {
	srv := defaultFolderServer(77)
	defer srv.Close()
	srv.Handle("/ajax/infostore", "new", testutil.Response{Body: map[string]any{"data": "77/9"}})
	srcPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(srcPath, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout, stderr, code := run(t, srv, filesEnv(), []string{"files", "create", "--file", srcPath, "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"folder_id":"77"`) {
		t.Errorf("stdout = %q, want folder_id 77", stdout)
	}
	for _, req := range srv.Requests() {
		if req.URL.Path != "/ajax/infostore" {
			continue
		}
		if err := req.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("parse upload: %v", err)
		}
		if meta := req.MultipartForm.Value["json"]; len(meta) != 1 || !strings.Contains(meta[0], `"folder_id":"77"`) {
			t.Errorf("upload metadata = %v, want folder_id 77", meta)
		}
	}
}

// An explicit empty or blank --folder is an error, not the default folder, and nothing is
// requested.
func TestFiles_EmptyFolderFlag_InvalidArgumentBeforeAnyRequest(t *testing.T) {
	srcPath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(srcPath, []byte("hello upload"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"list empty":          {"files", "list", "--folder", "", "--llm"},
		"list blank":          {"files", "list", "--folder", "  ", "--llm"},
		"create empty":        {"files", "create", "--file", srcPath, "--folder", "", "--llm"},
		"create blank dryrun": {"files", "create", "--file", srcPath, "--folder", " \t", "--dry-run", "--llm"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := filesTestServer()
			defer srv.Close()
			stdout, stderr, code := run(t, srv, filesEnv(), args)
			if code == 0 || stdout != "" || !strings.Contains(stderr, "invalid_argument") || !strings.Contains(stderr, "--folder") {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want invalid_argument for --folder", code, stdout, stderr)
			}
			if requests := srv.Requests(); len(requests) != 0 {
				t.Fatalf("requests = %d, want none; first %s", len(requests), requests[0].URL)
			}
		})
	}
}
