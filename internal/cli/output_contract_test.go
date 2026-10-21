// ABOUTME: Contract tests guarding the registry against output drift: every key a command
// ABOUTME: emits, on an object result or on a collection's rows, must be documented.
package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// outputContractCase drives one operation end to end and checks its machine-readable output
// against the operation's documented output fields.
type outputContractCase struct {
	operation string
	args      []string
	handle    func(*testutil.Server)
}

func TestRepeatedSelectFieldIsEmittedOnce(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, map[string]string{}, []string{
		"commands", "--format", "tsv", "--select", "name", "--select", "name",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	firstLine, _, _ := strings.Cut(stdout, "\n")
	if firstLine != "name" {
		t.Fatalf("TSV header = %q, want one name column", firstLine)
	}
}

// TestOperationOutput_DocumentsEveryEmittedKey is the third leg of the registry/parser
// contract, alongside the argument-name and bool-flag guards. --select is validated against
// an operation's registered Output.Fields before the request is sent, so a key a command
// actually returns but does not register is unreachable: "mbx mailfilter delete --id 7
// --confirm 7 --format json --select id" was rejected as "not documented for this command"
// even though the command's own output was {"deleted":true,"id":7}.
func TestOperationOutput_DocumentsEveryEmittedKey(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range outputContractCases() {
		t.Run(tc.operation, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			tc.handle(srv)

			stdout, stderr, code := run(t, srv, env, append(tc.args, "--llm"))
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}

			var emitted map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stdout), &emitted); err != nil {
				t.Fatalf("output is not a JSON object: %q", stdout)
			}

			op, ok := operations.Default.Get(tc.operation)
			if !ok {
				t.Fatalf("%s is not registered", tc.operation)
			}
			documented := map[string]bool{}
			for _, field := range op.Output.Fields {
				documented[field] = true
			}
			for key := range emitted {
				if !documented[key] {
					t.Errorf("%s emits %q but documents only %v; --select %s is rejected before the request",
						tc.operation, key, op.Output.Fields, key)
				}
			}
		})
	}
}

// TestOperationOutput_EveryDocumentedFieldIsSelectable drives the same calls through the
// --select path, which is what callers actually use, so a documented field that the renderer
// cannot find fails here rather than in a script.
func TestOperationOutput_EveryDocumentedFieldIsSelectable(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range outputContractCases() {
		t.Run(tc.operation, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			tc.handle(srv)

			plain, _, _ := run(t, srv, env, append(append([]string{}, tc.args...), "--llm"))
			var emitted map[string]json.RawMessage
			if err := json.Unmarshal([]byte(plain), &emitted); err != nil {
				t.Fatalf("output is not a JSON object: %q", plain)
			}

			for key := range emitted {
				srv2 := testutil.NewServer()
				srv2.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
				tc.handle(srv2)

				args := append(append([]string{}, tc.args...), "--format", "json", "--select", key)
				stdout, stderr, code := run(t, srv2, env, args)
				srv2.Close()

				if code != 0 {
					t.Errorf("--select %s: exit code = %d, want 0; stderr=%s", key, code, strings.TrimSpace(stderr))
					continue
				}
				if !strings.Contains(stdout, `"`+key+`"`) {
					t.Errorf("--select %s: stdout = %q, want the selected field", key, stdout)
				}
			}
		})
	}
}

func outputContractCases() []outputContractCase {
	return []outputContractCase{
		{
			operation: "mailfilter.delete",
			args:      []string{"mailfilter", "delete", "--id", "7", "--confirm", "7"},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/mailfilter", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "mailfilter.update",
			args:      []string{"mailfilter", "update", "--id", "7", "--rule", `{"active":false}`},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/mailfilter", "update", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "mailfilter.get",
			args:      []string{"mailfilter", "get", "--id", "7"},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/mailfilter", "list", testutil.Response{Body: map[string]any{"data": []any{
					map[string]any{
						"id": 7, "rulename": "keep", "active": true,
						"flags": []string{"vacation"}, "position": 2,
						"test":       map[string]any{"id": "true"},
						"actioncmds": []any{map[string]any{"id": "keep"}},
					},
				}}})
			},
		},
		{
			operation: "group.delete",
			args:      []string{"group", "delete", "--id", "5", "--confirm", "5"},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/group", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{"id": 5, "display_name": "Team", "last_modified_utc": 1700000000000},
				}})
				s.Handle("/ajax/group", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "events.delete",
			args:      []string{"events", "delete", "--id", "42", "--confirm", "42"},
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
			operation: "account.delete",
			args:      []string{"account", "delete", "--id", "5", "--confirm", "5"},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/account", "delete", testutil.Response{Body: map[string]any{"data": []any{5}}})
			},
		},
		{
			operation: "resources.delete",
			args:      []string{"resources", "delete", "--id", "5", "--confirm", "5"},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/resource", "get", testutil.Response{Body: map[string]any{
					"data": map[string]any{"id": 5, "last_modified_utc": 1700000000000},
				}})
				s.Handle("/ajax/resource", "delete", testutil.Response{Body: map[string]any{"data": nil}})
			},
		},
		{
			operation: "mail.send",
			args:      []string{"mail", "send", "--to", "fixture-user@example.invalid", "--subject", "hi", "--body", "there"},
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/mail", "new", testutil.Response{
					Body: []byte(`<script>(parent["callback_new"])({"data":"default0/INBOX/999"})</script>`),
				})
			},
		},
		{
			operation: "share.delete",
			args:      []string{"share", "delete", "--folder", "36", "--id", "3", "--confirm", "3"},
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
	}
}

// collectionContractCase drives one list command and checks the fields of the rows it
// returns, rather than the keys of the envelope around them.
type collectionContractCase struct {
	operation string
	args      []string
	// collection names the array field the command wraps its rows in.
	collection string
	handle     func(*testutil.Server)
}

// TestCollectionOutput_DocumentsEveryEmittedRowField extends the documented-output contract
// to list commands. --select on a collection projects the rows, not the envelope, so a row
// field the operation does not register is just as unreachable as an undocumented key on an
// object result: "mbx account list --format json --select login" was rejected as "not
// documented for this command" while every row carried a login.
func TestCollectionOutput_DocumentsEveryEmittedRowField(t *testing.T) {
	env := map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

	for _, tc := range collectionContractCases() {
		t.Run(tc.operation, func(t *testing.T) {
			srv := testutil.NewServer()
			defer srv.Close()
			srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
			tc.handle(srv)

			stdout, stderr, code := run(t, srv, env, append(append([]string{}, tc.args...), "--llm"))
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}

			var envelope map[string]json.RawMessage
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
				t.Fatalf("output is not a JSON object: %q", stdout)
			}
			var rows []map[string]json.RawMessage
			if err := json.Unmarshal(envelope[tc.collection], &rows); err != nil {
				t.Fatalf("%q is not an array of objects: %q", tc.collection, stdout)
			}
			if len(rows) == 0 {
				t.Fatalf("fixture returned no rows: %q", stdout)
			}

			op, ok := operations.Default.Get(tc.operation)
			if !ok {
				t.Fatalf("%s is not registered", tc.operation)
			}
			documented := map[string]bool{}
			for _, field := range op.Output.Fields {
				documented[field] = true
			}
			for _, row := range rows {
				for key := range row {
					if !documented[key] {
						t.Errorf("%s returns rows carrying %q but documents only %v; --select %s is rejected before the request",
							tc.operation, key, op.Output.Fields, key)
					}
				}
			}

			for _, field := range op.Output.Fields {
				args := append(append([]string{}, tc.args...), "--format", "json", "--select", field)
				out, selErr, selCode := run(t, srv, env, args)
				if selCode != 0 {
					t.Errorf("--select %s: exit code = %d, want 0; stderr=%s", field, selCode, strings.TrimSpace(selErr))
					continue
				}
				if !strings.Contains(out, `"`+field+`"`) {
					t.Errorf("--select %s: stdout = %q, want the selected field", field, out)
				}
			}
		})
	}
}

func collectionContractCases() []collectionContractCase {
	return []collectionContractCase{
		{
			operation:  "account.list",
			args:       []string{"account", "list"},
			collection: "accounts",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/account", "all", testutil.Response{Body: map[string]any{"data": []any{
					[]any{0, "Primary", "a@example.org", "a@example.org"},
				}}})
			},
		},
		{
			operation:  "mailfilter.list",
			args:       []string{"mailfilter", "list"},
			collection: "rules",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/mailfilter", "list", testutil.Response{Body: map[string]any{"data": []any{
					map[string]any{
						"id": 7, "rulename": "keep", "active": true,
						"flags": []string{"vacation"}, "position": 2,
						"test":       map[string]any{"id": "true"},
						"actioncmds": []any{map[string]any{"id": "keep"}},
					},
				}}})
			},
		},
		{
			operation:  "resources.search",
			args:       []string{"resources", "search"},
			collection: "resources",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/resource", "search", testutil.Response{Body: map[string]any{"data": []any{
					map[string]any{"id": 3, "display_name": "Room", "mailaddress": "room@example.org"},
				}}})
			},
		},
		{
			operation:  "snippet.list",
			args:       []string{"snippet", "list"},
			collection: "snippets",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/snippet", "all", testutil.Response{Body: map[string]any{"data": []any{
					map[string]any{
						"id": "2", "displayname": "Sig", "content": "Regards",
						"module": "io.ox/mail", "type": "signature", "shared": false, "createdby": 5,
					},
				}}})
			},
		},
		{
			operation:  "group.list",
			args:       []string{"group", "list"},
			collection: "groups",
			handle: func(s *testutil.Server) {
				s.Handle("/ajax/group", "search", testutil.Response{Body: map[string]any{"data": []any{
					map[string]any{"id": 5, "display_name": "Team", "last_modified_utc": 1700000000000, "members": []int{3}},
				}}})
			},
		},
	}
}
