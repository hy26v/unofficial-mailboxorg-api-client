// ABOUTME: CLI tests for "mail correspondents": the address search request, per-address counts,
// ABOUTME: names, roles and ordering, the limit, and argument validation.
package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// correspondentRow is one search row with the columns id, subject, from, date, to and cc.
func correspondentRow(id string, date int64, from []any, to, cc []any) []any {
	return []any{id, "Subject " + id, []any{from}, date, to, cc}
}

func newCorrespondentsServer(t *testing.T, rows []any) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{}}})
	srv.Handle("/ajax/mail", "search", testutil.Response{Body: map[string]any{"data": rows}})
	return srv
}

func TestMailCorrespondents_CountsDistinctMatchingAddresses(t *testing.T) {
	robin := []any{"Robin", "robin@example.org"}
	rows := []any{
		correspondentRow("3", 1700000300000, []any{"Ana Partner", "Ana@Partners.Example"}, []any{robin}, []any{}),
		correspondentRow("2", 1700000200000, robin, []any{[]any{"Info", "info@partners.example"}}, []any{[]any{"Ana P.", "ana@partners.example"}}),
		correspondentRow("1", 1700000100000, []any{nil, "info@partners.example"}, []any{robin}, []any{}),
	}
	srv := newCorrespondentsServer(t, rows)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "correspondents", "--query", "partners", "--folder", "default0/INBOX", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	want := `{"items":[` +
		`{"address":"ana@partners.example","name":"Ana Partner","count":2,"last_date":"2023-11-14T22:18:20Z","roles":["from","cc"],"from_count":1,"to_count":0,"cc_count":1,"last_from":"2023-11-14T22:18:20Z","last_to":"2023-11-14T22:16:40Z"},` +
		`{"address":"info@partners.example","name":"Info","count":2,"last_date":"2023-11-14T22:16:40Z","roles":["from","to"],"from_count":1,"to_count":1,"cc_count":0,"last_from":"2023-11-14T22:15:00Z","last_to":"2023-11-14T22:16:40Z"}` +
		`],"count":2,"total":2,"truncated":false,"has_more":false}` + "\n"
	if stdout != want {
		t.Errorf("stdout =\n%s\nwant\n%s", stdout, want)
	}

	search := srv.Requests()[2]
	if got := search.URL.Query().Get("columns"); got != "600,607,603,610,604,605" {
		t.Errorf("columns = %s, want id, subject, from, date, to and cc", got)
	}
	body, _ := io.ReadAll(search.Body)
	if want := `{"filter":["or",["=",{"field":"from"},"partners"],["=",{"field":"to"},"partners"],["=",{"field":"cc"},"partners"]]}`; string(body) != want {
		t.Errorf("search body = %s, want %s", body, want)
	}
}

func TestMailCorrespondents_LimitKeepsTheMostFrequent(t *testing.T) {
	robin := []any{"Robin", "robin@example.org"}
	rows := []any{
		correspondentRow("3", 1700000300000, []any{nil, "rare@example.net"}, []any{robin}, []any{}),
		correspondentRow("2", 1700000200000, []any{nil, "often@example.net"}, []any{robin}, []any{}),
		correspondentRow("1", 1700000100000, []any{nil, "often@example.net"}, []any{robin}, []any{}),
	}
	srv := newCorrespondentsServer(t, rows)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "correspondents", "--query", "example.net", "--folder", "default0/INBOX", "--limit", "1", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var doc mailCorrespondentsDocument
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Items) != 1 || doc.Items[0].Address != "often@example.net" || doc.Total != 2 || !doc.HasMore || !doc.Truncated {
		t.Errorf("document = %s, want often@example.net of two", stdout)
	}
}

func TestMailCorrespondents_Validation(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"missing query": {[]string{"mail", "correspondents", "--llm"}, "--query is required"},
		"bad in field":  {[]string{"mail", "correspondents", "--query", "x", "--in", "body", "--llm"}, `unsupported field \"body\"`},
		"limit range":   {[]string{"mail", "correspondents", "--query", "x", "--limit", "201", "--llm"}, "--limit must be an integer between 1 and 200"},
		"dates":         {[]string{"mail", "correspondents", "--query", "x", "--since", "2026-02-01T00:00:00Z", "--until", "2026-01-01T00:00:00Z", "--llm"}, "--since must be earlier"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := newCorrespondentsServer(t, nil)
			defer srv.Close()
			_, stderr, code := run(t, srv, mailSearchEnv, tc.args)
			if code != 4 || !strings.Contains(stderr, tc.want) {
				t.Errorf("exit %d, stderr %s; want 4 and %q", code, stderr, tc.want)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("invalid arguments sent %d requests", len(srv.Requests()))
			}
		})
	}
}

func TestMailCorrespondents_DefaultScopeLeavesOutDraftsTrashAndSpam(t *testing.T) {
	srv := newScopeServer(t)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "correspondents", "--query", "supplier.example", "--llm"})
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr)
	}
	if got := strings.Join(requestedFolders(srv, "search"), ","); got != "default0/INBOX,default0/Sent,"+scopeLegal+",default0/INBOX/Kit" {
		t.Errorf("searched folders = %s, want no Drafts, Trash or Junk", got)
	}
	var document mailCorrespondentsDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || len(document.Items) != 2 {
		t.Fatalf("stdout = %s, want two addresses", stdout)
	}
	support := document.Items[0]
	if support.Address != "support@supplier.example" || support.Count != 2 || support.FromCount != 1 || support.ToCount != 1 ||
		support.LastFrom != "2023-11-14T22:21:40Z" || support.LastTo != "2023-11-14T22:18:20Z" || support.LastDate != support.LastFrom {
		t.Errorf("support = %+v, want one received and one sent message, without the draft", support)
	}
	before := len(srv.Requests())
	if _, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "correspondents", "--query", "supplier.example", "--folder", "1", "--llm"}); code != 0 {
		t.Fatalf("folder 1: exit %d; stderr=%s", code, stderr)
	}
	searched := 0
	for _, req := range srv.Requests()[before:] {
		if req.URL.Query().Get("action") == "search" {
			searched++
		}
	}
	if searched != 8 {
		t.Errorf("folder 1 searched %d folders, want all 8", searched)
	}
}
