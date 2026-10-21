// ABOUTME: End-to-end tests for global output flags, local discovery, private file writes,
// ABOUTME: and strict list/share response decoding.
package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func TestLLMFlag_MayAppearAnywhereInCommand(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	tests := []struct {
		name  string
		args  []string
		setup func(*testutil.Server)
		want  string
	}{
		{
			name: "before top-level command",
			args: []string{"--llm", "commands"},
			want: `"commands":`,
		},
		{
			name: "between resource and action",
			args: []string{"contacts", "--llm", "list"},
			setup: func(s *testutil.Server) {
				s.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
				s.Handle("/ajax/user", "get", testutil.Response{
					Body: map[string]any{"data": map[string]any{"folder_id": "33"}},
				})
				s.Handle("/ajax/contacts", "all", testutil.Response{Body: map[string]any{"data": []any{}}})
			},
			want: `"contacts":`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			if tt.setup != nil {
				tt.setup(srv)
			}

			stdout, stderr, code := run(t, srv, env, tt.args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if !strings.Contains(stdout, tt.want) || !strings.HasPrefix(strings.TrimSpace(stdout), "{") {
				t.Fatalf("stdout = %q, want structured output containing %q", stdout, tt.want)
			}
		})
	}
}

func TestGlobalOutputFlags_DoNotCorruptHelpTargets(t *testing.T) {
	tests := map[string][]string{
		"llm before help":     {"--llm", "help", "contacts.list"},
		"llm after target":    {"help", "contacts.list", "--llm"},
		"format after target": {"help", "contacts.list", "--format", "json"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := run(t, srv, map[string]string{}, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if !strings.Contains(stdout, "contacts.list") {
				t.Fatalf("stdout = %q, want contacts.list help", stdout)
			}
			if stderr != "" {
				t.Fatalf("stderr = %q, want empty", stderr)
			}
		})
	}
}

func TestGlobalOutputBoundary_PreservesFlagLikeCommandValues(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"mail", "draft", "--format", "json", "--subject", "--", "--llm", "--dry-run",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"subject":"--llm"`) {
		t.Fatalf("stdout = %q, want literal --llm subject preserved", stdout)
	}
	if len(srv.Requests()) != 0 {
		t.Fatalf("dry run made HTTP requests: %+v", srv.Requests())
	}
}

func TestHelpFlagSpelling_IsPreservedAsACommandValue(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"mail", "draft", "--subject", "--help", "--dry-run", "--llm",
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"subject":"--help"`) || !strings.Contains(stdout, `"dry_run":true`) {
		t.Fatalf("stdout = %q, want dry-run draft with literal --help subject", stdout)
	}
	if got := len(srv.Requests()); got != 0 {
		t.Fatalf("requests = %d, want 0", got)
	}
}

func TestLocalDiscovery_RemainsAvailableWithMalformedConfig(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "commands", args: []string{"commands", "--llm"}, want: `"commands":`},
		{name: "describe", args: []string{"describe", "whoami", "--llm"}, want: `"name":"whoami"`},
		{name: "transformed commands", args: []string{"commands", "--format", "values", "--select", "name"}, want: "whoami\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()

			stdout, stderr, code := runWithConfig(
				t,
				srv,
				map[string]string{},
				tt.args,
				"this is not valid TOML",
			)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if !strings.Contains(stdout, tt.want) {
				t.Fatalf("stdout = %q, want %q", stdout, tt.want)
			}
			if len(srv.Requests()) != 0 {
				t.Fatalf("local discovery made HTTP requests: %+v", srv.Requests())
			}
		})
	}
}

func TestDocumentOutputs_TightenPermissionsOnExistingFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	tests := []struct {
		name    string
		content string
		args    func(string) []string
		setup   func(*testutil.Server)
	}{
		{
			name: "contacts export", content: testVCard,
			args: func(path string) []string {
				return []string{"contacts", "export", "--folder", "33", "--output", path, "--llm"}
			},
			setup: func(s *testutil.Server) {
				s.Handle("/ajax/export", "VCARD", testutil.Response{Body: []byte(testVCard)})
			},
		},
		{
			name: "events export", content: testICal,
			args: func(path string) []string {
				return []string{"events", "export", "--folder", "32", "--output", path, "--llm"}
			},
			setup: func(s *testutil.Server) {
				s.Handle("/ajax/export", "ICAL", testutil.Response{Body: []byte(testICal)})
			},
		},
		{
			name: "file download", content: "file contents",
			args: func(path string) []string {
				return []string{"files", "get", "--id", "36/2", "--save-to", path, "--llm"}
			},
			setup: func(s *testutil.Server) {
				s.Handle("/ajax/infostore", "get", testutil.Response{
					Body: map[string]any{"data": map[string]any{
						"id": "36/2", "folder_id": "36", "title": "file.txt",
						"filename": "file.txt", "file_mimetype": "text/plain",
						"file_size": 13, "last_modified": 1,
					}},
				})
				s.Handle("/ajax/infostore", "document", testutil.Response{Body: []byte("file contents")})
			},
		},
		{
			name: "attachment download", content: "attachment contents",
			args: func(path string) []string {
				return []string{
					"attachments", "get", "--module", "7", "--object-id", "4",
					"--folder", "33", "--id", "1", "--save-to", path, "--llm",
				}
			},
			setup: func(s *testutil.Server) {
				s.Handle("/ajax/attachment", "get", testutil.Response{
					Body: map[string]any{"data": map[string]any{
						"id": 1, "filename": "attachment.txt", "file_size": 19,
						"file_mimetype": "text/plain",
					}},
				})
				s.Handle("/ajax/attachment", "document", testutil.Response{Body: []byte("attachment contents")})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			tt.setup(srv)

			path := filepath.Join(t.TempDir(), "existing-output")
			if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
				t.Fatalf("seeding output: %v", err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatalf("making output permissive: %v", err)
			}

			stdout, stderr, code := run(t, srv, env, tt.args(path))
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stdout=%s stderr=%s", code, stdout, stderr)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading output: %v", err)
			}
			if string(got) != tt.content {
				t.Errorf("output content = %q, want %q", got, tt.content)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatalf("stating output: %v", err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("output mode = %#o, want 0600", perm)
			}
		})
	}
}

func TestAccountList_RejectsMalformedRowsInsteadOfDroppingOrZeroFillingThem(t *testing.T) {
	tests := map[string][]any{
		"short row":     {0, "Account"},
		"wrong id type": {"not-an-id", "Account", "a@example.org", "login"},
		"null id":       {nil, "Account", "a@example.org", "login"},
	}
	for name, row := range tests {
		t.Run(name, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			srv.Handle("/ajax/account", "all", testutil.Response{
				Body: map[string]any{"data": []any{row}},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, []string{"account", "list", "--llm"})
			if code == 0 {
				t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
			}
			if !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("stderr = %q, want invalid_response", stderr)
			}
		})
	}
}

func TestDecodeAttachmentRows_RejectsMissingIDsAndWrongColumnTypes(t *testing.T) {
	tests := map[string]string{
		"null collection":   `null`,
		"object missing id": `[{"filename":"a.txt","file_size":1,"file_mimetype":"text/plain"}]`,
		"tuple null id":     `[[null,"a.txt",1,"text/plain"]]`,
		"wrong filename":    `[[1,99,1,"text/plain"]]`,
		"wrong size":        `[[1,"a.txt","large","text/plain"]]`,
		"wrong mime type":   `[[1,"a.txt",1,99]]`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if rows, err := decodeAttachmentRows([]byte(raw)); err == nil {
				t.Fatalf("decodeAttachmentRows(%s) = %+v, nil; want error", raw, rows)
			}
		})
	}
}

func TestShareCreate_RejectsResponseWithoutURL(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/share/management", "getLink", testutil.Response{
		Body: map[string]any{"data": map[string]any{"entity": 4, "is_new": true}},
	})

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"share", "create", "--folder", "36", "--id", "4", "--llm",
	})
	if code == 0 {
		t.Fatalf("exit code = 0, want invalid response; stdout=%s", stdout)
	}
	if !strings.Contains(stderr, "invalid_response") {
		t.Fatalf("stderr = %q, want invalid_response", stderr)
	}
}
