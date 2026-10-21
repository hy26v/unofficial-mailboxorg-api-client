// ABOUTME: Cross-command contract test for what a successful delete reports: the same
// ABOUTME: boolean confirmation and the same identifier, whichever resource was deleted.
package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// deleteContractCase drives one destructive delete to a successful result.
type deleteContractCase struct {
	operation string
	args      []string
	handle    func(s *testutil.Server)
	// wantID is the identifier the command should report as deleted.
	wantID string
}

// TestDelete_ReportsTheSameConfirmationEverywhere is the machine-readable half of the
// destructive-command contract. A caller that deletes across resources - an agent cleaning up
// its own fixtures, a script unwinding a failed import - reads one field to decide whether
// the object is gone. Eleven commands answered {"deleted":true,...}; folders delete and
// reminder delete answered {"deleted":"<id>"}, where the id is a truthy string, so a caller
// testing deleted === true silently concluded the opposite of what happened. mail delete
// answered {"deleted":true} with no id at all, even though its own human output names one.
func TestDelete_ReportsTheSameConfirmationEverywhere(t *testing.T) {
	// Every destructive operation that reports a "deleted" field must appear below.
	pending := map[string]bool{}
	for _, op := range operations.Default.List() {
		if !operationIsDestructive(op) {
			continue
		}
		for _, field := range op.Output.Fields {
			if field == "deleted" {
				pending[op.Name] = true
			}
		}
	}
	for _, tc := range deleteContractCases() {
		delete(pending, tc.operation)
	}
	for name := range pending {
		t.Errorf("%s reports a deleted field but is not covered by this contract test", name)
	}

	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}
	for _, tc := range deleteContractCases() {
		t.Run(tc.operation, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			tc.handle(srv)

			stdout, stderr, code := run(t, srv, env, append(append([]string{}, tc.args...), "--llm"))
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, strings.TrimSpace(stderr))
			}

			var emitted map[string]any
			if err := json.Unmarshal([]byte(stdout), &emitted); err != nil {
				t.Fatalf("output is not a JSON object: %q", stdout)
			}
			if emitted["deleted"] != true {
				t.Errorf("deleted = %#v, want the boolean true", emitted["deleted"])
			}
			id, ok := emitted["id"]
			if !ok {
				t.Errorf("output names no id, so a caller cannot tell what was deleted: %s", strings.TrimSpace(stdout))
			} else if idString(id) != tc.wantID {
				t.Errorf("id = %#v, want %q", id, tc.wantID)
			}

			op, _ := operations.Default.Get(tc.operation)
			documented := map[string]bool{}
			for _, field := range op.Output.Fields {
				documented[field] = true
			}
			for _, field := range []string{"deleted", "id"} {
				if !documented[field] {
					t.Errorf("%s reports %q but documents only %v, so --select %s is rejected",
						tc.operation, field, op.Output.Fields, field)
				}
			}
		})
	}
}

// idString renders an id the same way whether the command reports it as a JSON string or a
// JSON number; both spellings are already in use and this contract is about "deleted".
func idString(id any) string {
	if number, ok := id.(float64); ok && number == float64(int64(number)) {
		return strings.TrimSuffix(strings.TrimRight(jsonNumber(number), "."), ".")
	}
	text, _ := id.(string)
	return text
}

func jsonNumber(n float64) string {
	out, _ := json.Marshal(n)
	return string(out)
}

func deleteContractCases() []deleteContractCase {
	return []deleteContractCase{
		{
			operation: "account.delete",
			args:      []string{"account", "delete", "--id", "5", "--confirm", "5"},
			wantID:    "5",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/account", "delete", testutil.Response{Body: map[string]any{"data": []any{5}}})
			},
		},
		{
			operation: "attachments.delete",
			args:      []string{"attachments", "delete", "--module", "7", "--object-id", "5", "--folder", "33", "--id", "9", "--confirm", "9"},
			wantID:    "9",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/attachment", "detach", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "contacts.delete",
			args:      []string{"contacts", "delete", "--id", "5", "--folder", "33", "--timestamp", "1", "--confirm", "5"},
			wantID:    "5",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/contacts", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})
			},
		},
		{
			operation: "events.delete",
			args:      []string{"events", "delete", "--id", "42", "--confirm", "42"},
			wantID:    "42",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{"id": "42"}, "timestamp": 1700000000000,
				}})
				s.Handle("/ajax/chronos", "delete", testutil.Response{Body: map[string]any{"data": []any{
					map[string]any{"folderId": "cal://0/32", "id": "42", "deleted": []any{map[string]any{"id": "42"}}},
				}}})
			},
		},
		{
			operation: "files.delete",
			args:      []string{"files", "delete", "--id", "36/2", "--confirm", "36/2"},
			wantID:    "36/2",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/infostore", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{"id": "36/2", "folder_id": "36", "last_modified": 1700000000000},
				}})
				s.Handle("/ajax/infostore", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})
			},
		},
		{
			operation: "folders.delete",
			args:      []string{"folders", "delete", "--id", "33/17", "--confirm", "33/17"},
			wantID:    "33/17",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/folders", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{
						"id": "33/17", "title": "Friends", "module": "contacts", "folder_id": "33",
						"subfolders": false, "last_modified": 1700000000000,
					},
				}})
				s.Handle("/ajax/folders", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})
			},
		},
		{
			operation: "group.delete",
			args:      []string{"group", "delete", "--id", "5", "--confirm", "5"},
			wantID:    "5",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/group", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{"id": 5, "display_name": "Team", "last_modified_utc": 1700000000000},
				}})
				s.Handle("/ajax/group", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "mail.delete",
			args:      []string{"mail", "delete", "--id", "12", "--folder", "default0/INBOX", "--confirm", "12"},
			wantID:    "12",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/mail", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})
			},
		},
		{
			operation: "mailfilter.delete",
			args:      []string{"mailfilter", "delete", "--id", "7", "--confirm", "7"},
			wantID:    "7",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/mailfilter", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "reminder.delete",
			args:      []string{"reminder", "delete", "--id", "9", "--confirm", "9"},
			wantID:    "9",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/reminder", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "resources.delete",
			args:      []string{"resources", "delete", "--id", "5", "--confirm", "5"},
			wantID:    "5",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/resource", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{"id": 5, "last_modified_utc": 1700000000000},
				}})
				s.Handle("/ajax/resource", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "share.delete",
			args:      []string{"share", "delete", "--folder", "36", "--id", "3", "--confirm", "3"},
			wantID:    "3",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/infostore", "list", testutil.Response{Body: map[string]any{
					"data": []any{[]any{"36/3", "36", []any{map[string]any{"type": "anonymous", "share_url": "https://example.invalid/s/abc"}}}},
				}})
				s.Handle("/ajax/share/management", "getLink", testutil.Response{Body: map[string]any{
					"data":      map[string]any{"url": "https://example.invalid/s/abc", "entity": 3, "is_new": false},
					"timestamp": 1700000000000,
				}})
				s.Handle("/ajax/share/management", "deleteLink", testutil.Response{Body: map[string]any{"data": map[string]any{}}})
			},
		},
		{
			operation: "snippet.delete",
			args:      []string{"snippet", "delete", "--id", "2", "--confirm", "2"},
			wantID:    "2",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/snippet", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "tasks.delete",
			args:      []string{"tasks", "delete", "--id", "4", "--timestamp", "1", "--confirm", "4"},
			wantID:    "4",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/folders", "list", testutil.Response{
					Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
				})
				s.Handle("/ajax/tasks", "delete", testutil.Response{Body: map[string]any{"data": []any{}}})
			},
		},
	}
}
