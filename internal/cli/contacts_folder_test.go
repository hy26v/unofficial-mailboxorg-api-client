// ABOUTME: Tests that contacts create honours a folder_id given as contact data, and that
// ABOUTME: contacts update refuses a folder_id that would silently move the contact.
package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// folder_id is an ordinary OX contact field, so --field folder_id=... and an --input object
// carrying one are both natural ways to say where the contact belongs. contacts create
// merged them into the request body and then overwrote the result with the account default,
// so the contact was filed somewhere the caller never asked for and the reported folder_id
// agreed with the request that was sent rather than the one that was asked for. Nothing in
// the output revealed the substitution.
func TestContactsCreate_HonoursFolderIDGivenAsAField(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"field flag", []string{"contacts", "create", "--field", "display_name=A B", "--field", "folder_id=99", "--llm"}},
		{"structured input", []string{"contacts", "create", "--input", `{"display_name":"A B","folder_id":"99"}`, "--llm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := contactsCRUDTestServer()
			defer srv.Close()
			srv.Handle("/ajax/contacts", "new", testutil.Response{
				Body: map[string]any{"data": map[string]any{"id": "7"}, "timestamp": 1700000000000},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, tc.args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}

			var sent map[string]any
			for _, r := range srv.Requests() {
				if r.URL.Path != "/ajax/contacts" || r.URL.Query().Get("action") != "new" {
					continue
				}
				body, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(body, &sent); err != nil {
					t.Fatalf("create body is not JSON: %s", body)
				}
			}
			if sent == nil {
				t.Fatal("no request to /ajax/contacts?action=new observed")
			}
			if sent["folder_id"] != "99" {
				t.Errorf("request folder_id = %v, want 99", sent["folder_id"])
			}
			if !strings.Contains(stdout, `"folder_id":"99"`) {
				t.Errorf("stdout = %q, want the folder the contact was created in", stdout)
			}
		})
	}
}

// Two different answers to the same question is a contradiction the command cannot honour,
// so it says so rather than letting one of them win silently.
func TestContactsCreate_ConflictingFolderIsRefusedWithoutNetworkCall(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "create", "--folder", "33", "--field", "folder_id=99", "--llm",
	})

	if code == 0 {
		t.Fatalf("exit code = 0, want nonzero; stdout=%q", stdout)
	}
	if !strings.Contains(stderr, "folder_id") {
		t.Errorf("stderr = %q, want the conflicting folder named", stderr)
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("made %d requests, want 0", len(srv.Requests()))
	}
}

// --folder alone keeps working, and so does neither, which falls back to the account default.
func TestContactsCreate_FolderFlagAndDefaultAreUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantFolder string
	}{
		{"explicit --folder", []string{"contacts", "create", "--folder", "44", "--field", "display_name=A B", "--llm"}, "44"},
		{"account default", []string{"contacts", "create", "--field", "display_name=A B", "--llm"}, "33"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := contactsCRUDTestServer()
			defer srv.Close()
			srv.Handle("/ajax/contacts", "new", testutil.Response{
				Body: map[string]any{"data": map[string]any{"id": "7"}, "timestamp": 1700000000000},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			stdout, stderr, code := run(t, srv, env, tc.args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}
			if !strings.Contains(stdout, `"folder_id":"`+tc.wantFolder+`"`) {
				t.Errorf("stdout = %q, want folder_id %s", stdout, tc.wantFolder)
			}
		})
	}
}

// A JSON null folder_id names no folder. It used to become the literal folder "<nil>".
func TestContactsCreate_NullFolderIDIsTreatedAsAbsent(t *testing.T) {
	srv := contactsCRUDTestServer()
	defer srv.Close()

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	stdout, stderr, code := run(t, srv, env, []string{
		"contacts", "create", "--input", `{"display_name":"A B","folder_id":null}`, "--folder", "44", "--dry-run", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
	}
	if strings.Contains(stdout, "<nil>") || !strings.Contains(stdout, `"folder_id":"44"`) {
		t.Errorf("stdout = %q, want folder 44 and no <nil> folder", stdout)
	}
}

// folder_id given to contacts update through --field or --input went into the request body
// unchecked. The server could then move the contact while the output still showed the old
// folder. update must refuse that before any request, in --dry-run too.
func TestContactsUpdate_FolderIDThatWouldMoveTheContactIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"field flag", []string{"--folder", "33", "--field", "folder_id=99"}},
		{"structured input", []string{"--folder", "33", "--input", `{"folder_id":99}`}},
		{"no target folder", []string{"--field", "folder_id=99"}},
		{"dry run", []string{"--folder", "33", "--field", "folder_id=99", "--dry-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := contactsCRUDTestServer()
			defer srv.Close()

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			args := append([]string{"contacts", "update", "--id", "5", "--timestamp", "1784862966136", "--llm"}, tc.args...)
			stdout, stderr, code := run(t, srv, env, args)
			if code != 4 {
				t.Fatalf("exit code = %d, want 4 (invalid_argument); stdout=%q", code, stdout)
			}
			if stdout != "" {
				t.Errorf("stdout = %q, want empty", stdout)
			}
			if !strings.Contains(stderr, "invalid_argument") || !strings.Contains(stderr, "does not support moving") {
				t.Errorf("stderr = %q, want invalid_argument saying update cannot move a contact", stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

// A folder_id equal to --folder, or null, changes nothing and is left out of the body.
func TestContactsUpdate_FolderIDThatMatchesTheTargetIsDropped(t *testing.T) {
	for _, input := range []string{`{"display_name":"Updated","folder_id":"33"}`, `{"display_name":"Updated","folder_id":null}`} {
		t.Run(input, func(t *testing.T) {
			srv := contactsCRUDTestServer()
			defer srv.Close()
			srv.Handle("/ajax/contacts", "update", testutil.Response{
				Body: map[string]any{"data": map[string]any{}, "timestamp": 1784862966999},
			})

			env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
			_, stderr, code := run(t, srv, env, []string{
				"contacts", "update", "--id", "5", "--folder", "33", "--timestamp", "1784862966136", "--input", input, "--llm",
			})
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}
			for _, r := range srv.Requests() {
				if r.URL.Path == "/ajax/contacts" && r.URL.Query().Get("action") == "update" {
					var sent map[string]any
					body, _ := io.ReadAll(r.Body)
					if err := json.Unmarshal(body, &sent); err != nil {
						t.Fatalf("update body is not JSON: %s", body)
					}
					if _, has := sent["folder_id"]; has || sent["display_name"] != "Updated" {
						t.Errorf("update body = %v, want display_name only", sent)
					}
					return
				}
			}
			t.Fatal("no request to /ajax/contacts?action=update observed")
		})
	}
}
