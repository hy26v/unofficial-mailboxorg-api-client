// ABOUTME: CLI tests for "mail search": the exact OX search request and filter, query parsing,
// ABOUTME: limit-bounded paging over provider matches, recursion from the private root, and validation.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func mailSearchRows(count int) []any {
	rows := make([]any, 0, count)
	for i := 1; i <= count; i++ {
		rows = append(rows, []any{
			fmt.Sprint(i), fmt.Sprintf("Invoice %d", i),
			[]any{[]any{nil, fmt.Sprintf("billing%d@example.org", i)}}, int64(1700000000000) - int64(i)*1000,
		})
	}
	return rows
}

func newMailSearchServer(t *testing.T, rows []any) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "search", testutil.Response{Body: map[string]any{"data": rows}})
	return srv
}

var mailSearchEnv = map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

func TestMailSearch_SendsExactSearchRequest(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(1))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", `invoice "March 2026"`, "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"subject":"Invoice 1"`) {
		t.Errorf("stdout = %s, want the matching message", stdout)
	}

	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want login + one search", len(reqs))
	}
	req := reqs[1]
	query := req.URL.Query()
	if req.Method != "PUT" || req.URL.Path != "/ajax/mail" || len(query) != 8 || query.Get("limit") != "0,21" ||
		query.Get("action") != "search" || query.Get("session") != "sess-1" || query.Get("timezone") != "UTC" ||
		query.Get("folder") != "default0/INBOX" || query.Get("columns") != mailListColumns ||
		query.Get("sort") != "610" || query.Get("order") != "desc" {
		t.Errorf("search request = %s %s, want exact search query", req.Method, req.URL)
	}
	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
	}
	body, _ := io.ReadAll(req.Body)
	alternatives := func(term string) string {
		return fmt.Sprintf(`["or",["=",{"field":"subject"},%[1]q],["=",{"field":"from"},%[1]q],["=",{"field":"to"},%[1]q],["=",{"field":"cc"},%[1]q],["=",{"field":"content"},%[1]q]]`, term)
	}
	want := `{"filter":["and",` + alternatives("invoice") + `,` + alternatives("March 2026") + `]}`
	if string(body) != want {
		t.Errorf("search body =\n%s\nwant\n%s", body, want)
	}
}

func TestMailSearch_SingleFieldSingleTermFilter(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(1))
	defer srv.Close()

	_, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "refund", "--in", "body", "--folder", "default0/Archive", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	req := srv.Requests()[1]
	body, _ := io.ReadAll(req.Body)
	if string(body) != `{"filter":["=",{"field":"content"},"refund"]}` {
		t.Errorf("search body = %s, want one body term", body)
	}
	if req.URL.Query().Get("folder") != "default0/Archive" {
		t.Errorf("folder = %q, want default0/Archive", req.URL.Query().Get("folder"))
	}
}

func TestMailSearch_PagesMatchesLocallyWithOneRequest(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(5))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--limit", "2", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var first mailListDocument
	if err := json.Unmarshal([]byte(stdout), &first); err != nil {
		t.Fatalf("stdout is not a list document: %v; %s", err, stdout)
	}
	if first.Count != 2 || first.Items[0].ID != "1" || first.Items[1].ID != "2" || !first.HasMore || first.NextCursor != "offset:2" {
		t.Fatalf("first page = %+v, want ids 1,2 and next_cursor offset:2", first)
	}

	stdout, stderr, code = run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--limit", "2", "--cursor", first.NextCursor, "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var second mailListDocument
	if err := json.Unmarshal([]byte(stdout), &second); err != nil {
		t.Fatalf("stdout is not a list document: %v", err)
	}
	if second.Count != 2 || second.Items[0].ID != "3" || second.Items[1].ID != "4" || second.NextCursor != "offset:4" {
		t.Errorf("second page = %+v, want ids 3,4 and next_cursor offset:4", second)
	}

	stdout, _, code = run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--all", "--page-size", "2", "--llm"})
	var all mailListDocument
	if code != 0 || json.Unmarshal([]byte(stdout), &all) != nil || all.Count != 5 || all.HasMore {
		t.Errorf("--all = %s, want all five matches", stdout)
	}

	searches := 0
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "search" {
			searches++
			if req.URL.Query().Has("left_hand_limit") || req.URL.Query().Has("right_hand_limit") {
				t.Errorf("search request %s sent provider limits; paging must stay local", req.URL)
			}
		}
	}
	if searches != 3 {
		t.Errorf("search requests = %d, want one per invocation", searches)
	}
}

// newLimitAwareMailSearchServer serves total search matches. When honorLimit is true it
// applies the "limit=<start>,<end>" pair like current OX (start inclusive, end exclusive);
// otherwise it ignores limit like OX 7.10.6 and returns every match.
func newLimitAwareMailSearchServer(t *testing.T, total int, honorLimit bool) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	limits := []string{}
	rows := mailSearchRows(total)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		switch r.URL.Path + "?" + query.Get("action") {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/mail?search":
			mu.Lock()
			limits = append(limits, query.Get("limit"))
			mu.Unlock()
			page := rows
			if honorLimit && query.Has("limit") {
				bounds := strings.Split(query.Get("limit"), ",")
				start, _ := strconv.Atoi(bounds[0])
				end, _ := strconv.Atoi(bounds[len(bounds)-1])
				if start > len(rows) {
					start = len(rows)
				}
				if end > len(rows) {
					end = len(rows)
				}
				page = rows[start:end]
			}
			writeJSONResponse(w, map[string]any{"data": page})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), limits...)
	}
}

func TestMailSearch_SendsLimitAndPagesTheSameWhetherOrNotTheServerHonorsIt(t *testing.T) {
	for _, honorLimit := range []bool{true, false} {
		t.Run(fmt.Sprintf("honor=%v", honorLimit), func(t *testing.T) {
			for _, tc := range []struct {
				name      string
				total     int
				args      []string
				wantIDs   string
				wantMore  bool
				wantLimit string
			}{
				{"first page with more", 5, []string{"--limit", "2"}, "1,2", true, "0,3"},
				{"page exactly at the end", 4, []string{"--limit", "2", "--cursor", "offset:2"}, "3,4", false, "0,5"},
				{"short last page", 3, []string{"--limit", "2", "--cursor", "offset:2"}, "3", false, "0,5"},
				{"offset past the end", 2, []string{"--limit", "2", "--offset", "4"}, "", false, "0,7"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					srv, limits := newLimitAwareMailSearchServer(t, tc.total, honorLimit)
					defer srv.Close()
					args := append([]string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--llm"}, tc.args...)
					stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), args)
					if code != 0 {
						t.Fatalf("exit=%d stderr=%s", code, stderr)
					}
					var document mailListDocument
					if err := json.Unmarshal([]byte(stdout), &document); err != nil {
						t.Fatalf("stdout is not a list document: %v; %s", err, stdout)
					}
					ids := []string{}
					for _, item := range document.Items {
						ids = append(ids, item.ID)
					}
					if strings.Join(ids, ",") != tc.wantIDs || document.HasMore != tc.wantMore {
						t.Errorf("ids=%v has_more=%v, want ids %s has_more=%v", ids, document.HasMore, tc.wantIDs, tc.wantMore)
					}
					if got := limits(); len(got) != 1 || got[0] != tc.wantLimit {
						t.Errorf("limit parameters = %q, want one request with limit=%s", got, tc.wantLimit)
					}
				})
			}
		})
	}
}

func TestMailSearch_AllScanGrowsLimitUntilTheMatchesEnd(t *testing.T) {
	for _, honorLimit := range []bool{true, false} {
		t.Run(fmt.Sprintf("honor=%v", honorLimit), func(t *testing.T) {
			srv, limits := newLimitAwareMailSearchServer(t, 9, honorLimit)
			defer srv.Close()
			stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--all", "--page-size", "2", "--llm"})
			if code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, stderr)
			}
			var document mailListDocument
			if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Count != 9 || document.HasMore {
				t.Fatalf("stdout = %s, want all nine matches once", stdout)
			}
			for i, item := range document.Items {
				if item.ID != strconv.Itoa(i+1) {
					t.Fatalf("item %d id = %s, want %d (no duplicates, no gaps)", i, item.ID, i+1)
				}
			}
			want := []string{"0,3", "0,6", "0,12"}
			if !honorLimit {
				// The first response already holds every match, so no second request is needed.
				want = want[:1]
			}
			if got := limits(); strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("limit parameters = %q, want %q", got, want)
			}
		})
	}
}

func TestMailSearch_AppliesMetadataFiltersToMatches(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(3))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--from", "BILLING2@example.org", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var document mailListDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Count != 1 || document.Items[0].ID != "2" {
		t.Errorf("stdout = %s, want only message 2", stdout)
	}
}

func TestMailSearch_RecursiveFromPrivateRootSkipsTheRoot(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(1))
	defer srv.Close()
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{
		[]any{"default0/INBOX", "Inbox", "mail", 1, true},
		[]any{"default0/Archive", "Archive", "mail", 1, false},
		[]any{"33", "Contacts", "contacts", 1, false},
	}}})

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "invoice", "--folder", "1", "--all", "--recursive", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	searched := []string{}
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "search" {
			searched = append(searched, req.URL.Query().Get("folder"))
		}
	}
	if strings.Join(searched, ",") != "default0/INBOX,default0/Archive" {
		t.Errorf("searched folders = %v, want only the mail folders below the private root", searched)
	}
	var document mailListDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Count != 2 || document.Items[0].Folder == "" {
		t.Errorf("stdout = %s, want one match per folder with folder ids", stdout)
	}
}

func TestMailSearch_Validation(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()

	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"missing query":      {[]string{"mail", "search", "--folder", "default0/INBOX", "--llm"}, "--query is required"},
		"blank query":        {[]string{"mail", "search", "--folder", "default0/INBOX", "--query", "  ", "--llm"}, "--query must not be blank"},
		"empty quotes":       {[]string{"mail", "search", "--folder", "default0/INBOX", "--query", `""`, "--llm"}, "--query must not be blank"},
		"unclosed quote":     {[]string{"mail", "search", "--folder", "default0/INBOX", "--query", `"refund`, "--llm"}, "unclosed double quote"},
		"control character":  {[]string{"mail", "search", "--folder", "default0/INBOX", "--query", "a\x1bb", "--llm"}, "control characters"},
		"too many terms":     {[]string{"mail", "search", "--folder", "default0/INBOX", "--query", "a b c d e f g h i j k", "--llm"}, "maximum is 10"},
		"unknown field":      {[]string{"mail", "search", "--folder", "default0/INBOX", "--query", "x", "--in", "subject,headers", "--llm"}, `unsupported field \"headers\"`},
		"query on mail list": {[]string{"mail", "list", "--query", "x", "--llm"}, "unknown flag: --query"},
		"in on mail list":    {[]string{"mail", "list", "--in", "body", "--llm"}, "unknown flag: --in"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout, stderr, code := run(t, srv, mailSearchEnv, tc.args)
			if code == 0 || stdout != "" || !strings.Contains(stderr, `"invalid_argument"`) || !strings.Contains(stderr, tc.want) {
				t.Errorf("code=%d stdout=%q stderr=%q, want invalid_argument containing %q", code, stdout, stderr, tc.want)
			}
		})
	}
	if len(srv.Requests()) != 0 {
		t.Errorf("validation made %d requests, want none", len(srv.Requests()))
	}
}

func TestParseMailSearchQuery(t *testing.T) {
	terms, err := parseMailSearchQuery(`  invoice  "late fee"refund  `)
	if err != nil || strings.Join(terms, "|") != "invoice|late fee|refund" {
		t.Errorf("terms = %q, err = %v", terms, err)
	}
}

// mailSearchRowsWithTo adds the to column (604) that --fields to requests.
func mailSearchRowsWithTo(count int) []any {
	rows := mailSearchRows(count)
	for i, row := range rows {
		rows[i] = append(row.([]any), []any{[]any{"Robin", "robin@example.org"}})
	}
	return rows
}

func TestMailSearch_SelectOfAnOnRequestFieldRequestsIt(t *testing.T) {
	for name, args := range map[string][]string{
		"without --fields":     {"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--format", "json", "--select", "id,to"},
		"merged into --fields": {"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--fields", "to", "--format", "json", "--select", "id,to,cc"},
		"trailing value flag":  {"--format", "json", "--select", "id,to", "--", "mail", "search", "--folder", "default0/INBOX", "--limit", "5", "--query", "invoice"},
	} {
		t.Run(name, func(t *testing.T) {
			rows := mailSearchRowsWithTo(1)
			if strings.Contains(strings.Join(args, " "), "cc") {
				rows[0] = append(rows[0].([]any), []any{})
			}
			srv := newMailSearchServer(t, rows)
			defer srv.Close()
			stdout, stderr, code := run(t, srv, mailSearchEnv, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			if !strings.Contains(stdout, `"robin@example.org"`) {
				t.Errorf("stdout = %s, want the selected to field", stdout)
			}
			columns := srv.Requests()[1].URL.Query().Get("columns")
			if !strings.Contains(columns, "604") {
				t.Errorf("columns = %s, want the to column 604", columns)
			}
		})
	}
}

func TestMailSearch_FitReturnsTheNewestMatchesThatFitWithAnExactCursor(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(40))
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--all", "--fit", "--max-output-bytes", "1000", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if len(stdout) > 1000 {
		t.Errorf("output is %d bytes, over --max-output-bytes", len(stdout))
	}
	var doc struct {
		Items      []struct{ ID string }
		Count      int
		HasMore    bool   `json:"has_more"`
		Truncated  bool   `json:"truncated"`
		Available  int    `json:"available"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	kept := len(doc.Items)
	if kept == 0 || kept >= 40 || doc.Count != kept || !doc.HasMore || !doc.Truncated || doc.Available != 40 {
		t.Fatalf("document = %+v, want a trimmed page of 40", doc)
	}
	if doc.Items[0].ID != "1" || doc.NextCursor != "offset:"+strconv.Itoa(kept) {
		t.Errorf("first id %s, cursor %q, want 1 and offset:%d", doc.Items[0].ID, doc.NextCursor, kept)
	}
}

func TestMailSearch_FitCursorSkipsFilteredMatches(t *testing.T) {
	rows := mailSearchRows(30)
	for i := range rows {
		if i%2 == 1 {
			rows[i].([]any)[2] = []any{[]any{nil, "other@example.org"}}
		} else {
			rows[i].([]any)[2] = []any{[]any{nil, "billing@example.org"}}
		}
	}
	srv := newMailSearchServer(t, rows)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--from", "billing@example.org", "--limit", "15", "--fit", "--max-output-bytes", "600", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var doc struct {
		Items      []struct{ ID string }
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatal(err)
	}
	kept := len(doc.Items)
	if kept == 0 || kept >= 15 {
		t.Fatalf("kept %d items, want a trimmed page: %s", kept, stdout)
	}
	// Matches 0, 2, 4, ... pass --from; the first dropped one is match 2*kept.
	if want := "offset:" + strconv.Itoa(2*kept); doc.NextCursor != want {
		t.Errorf("next_cursor = %q, want %q", doc.NextCursor, want)
	}
}

func TestMailSearch_WithoutFitALargeResultStillFails(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(40))
	defer srv.Close()
	_, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--all", "--max-output-bytes", "1000", "--llm"})
	if code == 0 || !strings.Contains(stderr, "output_too_large") {
		t.Errorf("exit %d, stderr %s, want output_too_large", code, stderr)
	}
}

func TestMailSearch_SelectKeepsTheCursorOfAnIncompleteList(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(5))
	defer srv.Close()
	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--limit", "2", "--format", "json", "--select", "id"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if want := `{"has_more":true,"items":[{"id":"1"},{"id":"2"}],"next_cursor":"offset:2","next_offset":2,"truncated":true}` + "\n"; stdout != want {
		t.Errorf("stdout = %s, want %s", stdout, want)
	}
}

func TestParseMailSearchFields_AddressShorthand(t *testing.T) {
	fields, err := parseMailSearchFields("subject,address,to")
	if err != nil || strings.Join(fields, ",") != "subject,from,to,cc,bcc" {
		t.Errorf("fields = %v, %v; want subject,from,to,cc,bcc", fields, err)
	}
}

func TestMailSearch_RecursiveLimitKeepsTheNewestWithACursor(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(3))
	defer srv.Close()
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{
		[]any{"default0/INBOX", "Inbox", "mail", 1, false},
		[]any{"default0/Archive", "Archive", "mail", 1, false},
	}}})

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "invoice", "--folder", "1", "--recursive", "--limit", "4", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var document mailListDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatal(err)
	}
	if document.Count != 4 || !document.HasMore || !document.Truncated || document.Available != 6 || document.Limit != 4 || !strings.HasPrefix(document.NextCursor, "at:") {
		t.Errorf("stdout = %s, want the newest 4 of 6 matches with a cursor", stdout)
	}
	for i := 1; i < len(document.Items); i++ {
		if document.Items[i-1].Date < document.Items[i].Date {
			t.Errorf("items are not newest first: %s", stdout)
		}
	}
}

func TestMailSearch_AllWithLimitPagesLikeLimit(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRowsWithTo(5))
	defer srv.Close()
	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--all", "--limit", "2", "--fields", "from,to", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var document mailListDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Count != 2 || document.NextCursor != "offset:2" {
		t.Errorf("stdout = %s, want the first page of 2 with a cursor", stdout)
	}
	if columns := srv.Requests()[1].URL.Query().Get("columns"); columns != mailListColumns+",604" {
		t.Errorf("columns = %s, want the default columns plus to; from is always returned", columns)
	}
}

func TestMailSearch_ReturnsTheFolderByDefault(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(1))
	defer srv.Close()
	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"folder":"default0/INBOX"`) {
		t.Fatalf("code %d, stdout = %s, stderr = %s; want the folder", code, stdout, stderr)
	}
	stdout, _, code = run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice"})
	if code != 0 || !strings.Contains(stdout, "folder") || !strings.Contains(stdout, "default0/INBOX") {
		t.Errorf("human output = %q, want a folder column", stdout)
	}
}

func TestMailSearch_FromDomainFiltersOnTheServerAndExactly(t *testing.T) {
	rows := mailSearchRows(3)
	rows[1].([]any)[2] = []any{[]any{"Grants", "startups@example.org"}}
	rows[2].([]any)[2] = []any{[]any{nil, "news@mail.example.org"}}
	srv := newMailSearchServer(t, rows)
	defer srv.Close()
	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "grant", "--from-domain", "@Example.org", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	var document mailListDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Count != 2 || document.Items[0].From != "billing1@example.org" || document.Items[1].From != "startups@example.org" {
		t.Errorf("stdout = %s, want only the senders at example.org", stdout)
	}
	body, _ := io.ReadAll(srv.Requests()[1].Body)
	if !strings.Contains(string(body), `["=",{"field":"from"},"example.org"]`) {
		t.Errorf("search body = %s, want the sender domain term", body)
	}
}

func TestMailSearch_SkipTrashLeavesOutTrashAndSpam(t *testing.T) {
	srv := newMailSearchServer(t, mailSearchRows(1))
	defer srv.Close()
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{
		[]any{"default0/INBOX", "Inbox", "mail", 1, false},
		[]any{"default0/Trash", "Trash", "mail", 1, false},
		[]any{"default0/Trash/Old", "Old", "mail", 1, false},
		[]any{"default0/Junk", "Junk", "mail", 1, false},
		[]any{"default0/Trashy", "Trashy", "mail", 1, false},
	}}})
	srv.Handle("/ajax/account", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 0, "trash_fullname": "Trash", "spam_fullname": "Junk",
	}}})
	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "invoice", "--folder", "1", "--recursive", "--skip-trash", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s; stdout=%s", code, stderr, stdout)
	}
	var searched []string
	for _, req := range srv.Requests() {
		if req.URL.Query().Get("action") == "search" {
			searched = append(searched, req.URL.Query().Get("folder"))
		}
	}
	if strings.Join(searched, ",") != "default0/INBOX,default0/Trashy" {
		t.Errorf("searched folders = %v, want Trash, its subfolder and Spam left out", searched)
	}
	// mailbox.org leaves the *_fullname fields empty and names the folders in trash and spam.
	srv.Handle("/ajax/account", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 0, "trash_fullname": "", "spam_fullname": "", "trash": "Trash", "spam": "Junk",
	}}})
	searched = nil
	before := len(srv.Requests())
	if _, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "invoice", "--folder", "1", "--recursive", "--skip-trash", "--llm"}); code != 0 {
		t.Fatalf("names only: code %d, stderr=%s", code, stderr)
	}
	for _, req := range srv.Requests()[before:] {
		if req.URL.Query().Get("action") == "search" {
			searched = append(searched, req.URL.Query().Get("folder"))
		}
	}
	if strings.Join(searched, ",") != "default0/INBOX,default0/Trashy" {
		t.Errorf("names only: searched folders = %v, want Trash, its subfolder and Junk left out", searched)
	}
	if _, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--folder", "default0/INBOX", "--query", "invoice", "--skip-trash", "--llm"}); code != 4 || !strings.Contains(stderr, "--skip-trash needs --recursive") {
		t.Errorf("without --recursive: code %d, stderr = %s", code, stderr)
	}
}
