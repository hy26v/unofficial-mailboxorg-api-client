// ABOUTME: Tests that a download reports how many bytes it wrote even when the answer is
// ABOUTME: zero, so the documented bytes_written field is always there to select.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// bytes_written was serialised with omitempty, so a file that is genuinely empty - a
// placeholder, a truncated log, a zero-length upload - reported saved_to and then dropped the
// count. That is the one case where the number matters most, because it is the only signal
// that the download really did produce nothing, and the field is documented, so "--select
// bytes_written" failed outright with "not present in the result" rather than answering 0.
func TestDownload_ReportsZeroBytesWritten(t *testing.T) {
	for _, tc := range []struct {
		operation string
		args      func(saveTo string) []string
		handle    func(s *testutil.Server)
	}{
		{
			operation: "files.get",
			args: func(saveTo string) []string {
				return []string{"files", "get", "--id", "36/2", "--save-to", saveTo}
			},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{
						"id": "36/2", "folder_id": "36", "title": "Empty", "filename": "empty.txt",
						"file_mimetype": "text/plain", "file_size": 0, "last_modified": 1700000000000,
					},
				}})
				s.Handle("/ajax/infostore", "document", testutil.Response{Body: []byte{}})
			},
		},
		{
			operation: "attachments.get",
			args: func(saveTo string) []string {
				return []string{"attachments", "get", "--module", "7", "--object-id", "5", "--folder", "33", "--id", "9", "--save-to", saveTo}
			},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/attachment", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{
						"id": 9, "filename": "empty.txt", "file_size": 0, "file_mimetype": "text/plain",
					},
				}})
				s.Handle("/ajax/attachment", "document", testutil.Response{Body: []byte{}})
			},
		},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			saveTo := filepath.Join(t.TempDir(), "downloaded.txt")

			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			tc.handle(srv)

			stdout, stderr, code := run(t, srv, env, append(tc.args(saveTo), "--llm"))
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}
			if !strings.Contains(stdout, `"bytes_written":0`) {
				t.Errorf("stdout = %q, want bytes_written:0", stdout)
			}
			// The stored size is the same kind of measurement and had the same problem.
			for _, sizeField := range []string{`"size":0`, `"file_size":0`} {
				if strings.Contains(stdout, strings.Split(sizeField, ":")[0]) && !strings.Contains(stdout, sizeField) {
					t.Errorf("stdout = %q, want %s for an empty document", stdout, sizeField)
				}
			}

			written, err := os.ReadFile(saveTo)
			if err != nil {
				t.Fatalf("reading downloaded file: %v", err)
			}
			if len(written) != 0 {
				t.Errorf("wrote %d bytes, want 0", len(written))
			}

			// The documented field must be selectable, which is the path that actually broke.
			srv2 := testutil.NewServer()
			defer srv2.Close()
			srv2.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			tc.handle(srv2)

			args := append(tc.args(saveTo), "--format", "json", "--select", "bytes_written")
			stdout, stderr, code = run(t, srv2, env, args)
			if code != 0 {
				t.Fatalf("--select bytes_written: exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}
			if !strings.Contains(stdout, `"bytes_written":0`) {
				t.Errorf("--select bytes_written: stdout = %q, want bytes_written:0", stdout)
			}
		})
	}
}

// Without --save-to nothing is written, so neither field belongs in the result.
func TestDownload_OmitsSaveFieldsWhenNothingWasSaved(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{
		"data": map[string]any{
			"id": "36/2", "folder_id": "36", "title": "Doc", "filename": "doc.txt",
			"file_mimetype": "text/plain", "file_size": 12, "last_modified": 1700000000000,
		},
	}})

	stdout, stderr, code := run(t, srv, env, []string{"files", "get", "--id", "36/2", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
	}
	for _, unwanted := range []string{"saved_to", "bytes_written"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("stdout = %q, want no %q when nothing was saved", stdout, unwanted)
		}
	}
}
