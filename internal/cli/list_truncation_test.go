// ABOUTME: Cross-command contract test for bounded list output: a command that accepts
// ABOUTME: --limit must cap its result at that limit and say whether the cap cut it short.
package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// boundedListCase drives one --limit-accepting list command against a server holding a
// chosen number of rows.
type boundedListCase struct {
	operation string
	args      []string
	// handle registers the responses for a folder holding rows items.
	handle func(s *testutil.Server, rows int)
	// items names the array field the command wraps its rows in.
	items string
}

// TestBoundedListCommands_ReportTruncation is the contract behind every --limit flag: a
// response of exactly --limit rows is indistinguishable from a folder that holds exactly
// that many, so a caller cannot tell a complete list from a cut-off one unless the command
// says so. contacts list was the only bounded list command that neither capped its output
// locally nor reported truncation, so an agent listing a 200-contact folder saw 50 rows and
// no indication that any were missing.
func TestBoundedListCommands_ReportTruncation(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	// Every operation that documents a --limit must appear below.
	bounded := map[string]bool{}
	for _, op := range operations.Default.List() {
		if _, ok := op.Args["limit"]; ok {
			bounded[op.Name] = true
		}
	}
	for _, tc := range boundedListCases() {
		delete(bounded, tc.operation)
	}
	for name := range bounded {
		t.Errorf("%s accepts --limit but is not covered by this contract test", name)
	}

	for _, tc := range boundedListCases() {
		t.Run(tc.operation, func(t *testing.T) {
			for _, sc := range []struct {
				name          string
				rows          int
				wantItems     int
				wantTruncated bool
			}{
				{"more rows than the limit", 4, 2, true},
				{"exactly the limit", 2, 2, false},
				{"fewer rows than the limit", 1, 1, false},
			} {
				t.Run(sc.name, func(t *testing.T) {
					srv := testutil.NewServer()
					defer srv.Close()
					srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
					tc.handle(srv, sc.rows)

					args := append(append([]string{}, tc.args...), "--limit", "2", "--llm")
					stdout, stderr, code := run(t, srv, env, args)
					if code != 0 {
						t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
					}

					var out map[string]json.RawMessage
					if err := json.Unmarshal([]byte(stdout), &out); err != nil {
						t.Fatalf("output is not a JSON object: %q", stdout)
					}

					rawTruncated, ok := out["truncated"]
					if !ok {
						t.Fatalf("%s output has no \"truncated\" field: %s", tc.operation, stdout)
					}
					var truncated bool
					if err := json.Unmarshal(rawTruncated, &truncated); err != nil {
						t.Fatalf("truncated is not a boolean: %s", rawTruncated)
					}
					if truncated != sc.wantTruncated {
						t.Errorf("truncated = %v, want %v; output=%s", truncated, sc.wantTruncated, stdout)
					}

					var items []json.RawMessage
					if err := json.Unmarshal(out[tc.items], &items); err != nil {
						t.Fatalf("%q is not an array: %s", tc.items, stdout)
					}
					if len(items) != sc.wantItems {
						t.Errorf("returned %d rows, want at most the requested limit (%d); output=%s",
							len(items), sc.wantItems, stdout)
					}
				})
			}
		})
	}
}

// TestBoundedListCommands_HumanModeSaysItWasCutShort is the same contract for the default
// output mode. Every bounded list command computed "truncated" and then told only --llm
// callers about it, so "mbx mail list --limit 20" printed twenty rows and stopped: a folder
// holding exactly twenty messages and one holding two thousand produced identical output, and
// the person reading it had nothing to go on. The value was already in hand; only the human
// rendering dropped it.
func TestBoundedListCommands_HumanModeSaysItWasCutShort(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range boundedListCases() {
		t.Run(tc.operation, func(t *testing.T) {
			for _, sc := range []struct {
				name          string
				rows          int
				wantTruncated bool
			}{
				{"more rows than the limit", 4, true},
				{"exactly the limit", 2, false},
			} {
				t.Run(sc.name, func(t *testing.T) {
					srv := testutil.NewServer()
					defer srv.Close()
					srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
					tc.handle(srv, sc.rows)

					args := append(append([]string{}, tc.args...), "--limit", "2")
					stdout, stderr, code := run(t, srv, env, args)
					if code != 0 {
						t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
					}

					said := strings.Contains(stdout, "--limit")
					if said != sc.wantTruncated {
						if sc.wantTruncated {
							t.Errorf("%s returned more rows than --limit and said nothing about it: %q",
								tc.operation, stdout)
						} else {
							t.Errorf("%s was not cut short but claims it was: %q", tc.operation, stdout)
						}
					}
				})
			}
		})
	}
}

func boundedListCases() []boundedListCase {
	return []boundedListCase{
		{
			operation: "mail.list",
			args:      []string{"mail", "list"},
			items:     "items",
			handle: func(s *testutil.Server, rows int) {
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, []any{
						fmt.Sprint(i), fmt.Sprintf("Subject %d", i),
						[]any{[]any{nil, fmt.Sprintf("p%d@example.org", i)}}, 1700000000000,
					})
				}
				s.Handle("/ajax/mail", "all", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "mail.search",
			args:      []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice"},
			items:     "items",
			handle: func(s *testutil.Server, rows int) {
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, []any{
						fmt.Sprint(i), fmt.Sprintf("Invoice %d", i),
						[]any{[]any{nil, fmt.Sprintf("p%d@example.org", i)}}, 1700000000000,
					})
				}
				s.Handle("/ajax/mail", "search", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "mail.correspondents",
			args:      []string{"mail", "correspondents", "--query", "example.org", "--folder", "default0/INBOX"},
			items:     "items",
			handle: func(s *testutil.Server, rows int) {
				s.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{}}})
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, []any{
						fmt.Sprint(i), fmt.Sprintf("Invoice %d", i),
						[]any{[]any{nil, fmt.Sprintf("p%d@example.org", i)}}, 1700000000000, []any{}, []any{},
					})
				}
				s.Handle("/ajax/mail", "search", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "mail.thread",
			args:      []string{"mail", "thread", "--id", "1", "--folder", "default0/INBOX"},
			items:     "items",
			handle: func(s *testutil.Server, rows int) {
				s.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{[]any{"default0/INBOX", "Inbox", "mail", 1, false}}}})
				s.Handle("/ajax/account", "get", testutil.Response{Body: map[string]any{"data": map[string]any{"id": 0}}})
				s.Handle("/ajax/mail", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
					"id": "1", "folder_id": "default0/INBOX", "subject": "Re: Invoice", "flags": 0, "attachments": []any{},
				}}})
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, []any{
						fmt.Sprint(i), "Invoice", []any{[]any{nil, fmt.Sprintf("p%d@example.org", i)}}, 1700000000000 - int64(i), []any{},
					})
				}
				s.Handle("/ajax/mail", "search", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "contacts.list",
			args:      []string{"contacts", "list"},
			items:     "contacts",
			handle: func(s *testutil.Server, rows int) {
				s.Handle("/ajax/user", "get", testutil.Response{
					Body: map[string]any{"data": map[string]any{"folder_id": "33"}},
				})
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, []any{
						fmt.Sprint(i), fmt.Sprintf("Person %d", i), "P", fmt.Sprint(i),
						fmt.Sprintf("p%d@example.org", i),
					})
				}
				s.Handle("/ajax/contacts", "all", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "files.list",
			args:      []string{"files", "list", "--folder", "36"},
			items:     "files",
			handle: func(s *testutil.Server, rows int) {
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, []any{
						fmt.Sprint(i), "36", fmt.Sprintf("Doc %d", i),
						fmt.Sprintf("doc%d.txt", i), "text/plain", 10,
					})
				}
				s.Handle("/ajax/infostore", "all", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "tasks.list",
			args:      []string{"tasks", "list"},
			items:     "items",
			handle: func(s *testutil.Server, rows int) {
				s.Handle("/ajax/folders", "list", testutil.Response{
					Body: map[string]any{"data": []any{[]any{"35", "tasks"}}},
				})
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, []any{i, fmt.Sprintf("Task %d", i), 1700000000000, 1})
				}
				s.Handle("/ajax/tasks", "all", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "events.list",
			args:      []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z"},
			items:     "items",
			handle: func(s *testutil.Server, rows int) {
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, map[string]any{"id": fmt.Sprint(i), "summary": fmt.Sprintf("Event %d", i)})
				}
				s.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
		{
			operation: "reminder.list",
			args:      []string{"reminder", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z"},
			items:     "items",
			handle: func(s *testutil.Server, rows int) {
				data := make([]any, 0, rows)
				for i := 1; i <= rows; i++ {
					data = append(data, map[string]any{
						"id": i, "module": 4, "target_id": i, "folder": 35, "alarm": 1784844000000,
					})
				}
				s.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": data}})
			},
		},
	}
}
