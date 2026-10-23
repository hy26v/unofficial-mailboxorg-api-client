// ABOUTME: CLI tests for the scope and paging of mail search and mail list: folder all, the
// ABOUTME: recursive cursor, offsets, snippets, from_name, to for Sent, and unescaped folder ids.
package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

// scopeMessage is one message of the fake mailbox that newScopeServer serves.
type scopeMessage struct {
	id, subject, fromName, from, to string
	date                            int64
	body                            string
}

const scopeLegal = "default0/Example Ltd./Legal & Tax"

// scopeMailbox has messages in the standard folders and in a folder whose id has a space
// and "&". The same id is used in more than one folder, as on a real server.
var scopeMailbox = map[string][]scopeMessage{
	"default0/INBOX":     {{id: "1", subject: "Project Atlas offer", fromName: "Sam Taylor", from: "support@supplier.example", date: 1700000500000, body: "Hello   Robin,\n\nthe offer\tis attached."}},
	"default0/INBOX/Kit": {{id: "1", subject: "Re: Project Atlas", fromName: "", from: "r.smith@supplier.example", date: 1700000400000, body: "Second"}},
	"default0/Sent":      {{id: "1", subject: "Re: Project Atlas", fromName: "Admin", from: "admin@example.org", to: "support@supplier.example", date: 1700000300000, body: "Sent text"}},
	scopeLegal:           {{id: "2", subject: "Project Atlas contract", fromName: "Legal", from: "legal@example.org", date: 1700000300000, body: "Contract"}},
	"default0/Drafts":    {{id: "9", subject: "Project Atlas draft", from: "admin@example.org", to: "support@supplier.example", date: 1700000600000}},
	"default0/Trash":     {{id: "3", subject: "Project Atlas old", from: "support@supplier.example", date: 1700000550000}},
	"default0/Trash/Old": {{id: "4", subject: "Project Atlas older", from: "support@supplier.example", date: 1700000540000}},
	"default0/Junk":      {{id: "5", subject: "Project Atlas spam", from: "spam@example.net", date: 1700000530000}},
}

// newScopeServer serves scopeMailbox: the folder tree, the account's standard folders,
// one search or list result per folder with the requested columns, and mail get.
func newScopeServer(t *testing.T) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	folderRow := func(id string, subfolders bool) []any {
		return []any{id, id[strings.LastIndex(id, "/")+1:], "mail", 1, subfolders}
	}
	tree := map[string][]any{
		"1": {
			folderRow("default0/INBOX", true), folderRow("default0/Sent", false), folderRow("default0/Drafts", false),
			folderRow("default0/Trash", true), folderRow("default0/Junk", false), folderRow(scopeLegal, false),
			[]any{"33", "Contacts", "contacts", 1, false},
		},
		"default0/INBOX": {folderRow("default0/INBOX/Kit", false)},
		"default0/Trash": {folderRow("default0/Trash/Old", false)},
	}
	srv.HandleFunc("/ajax/folders", "list", func(r *http.Request) testutil.Response {
		children, ok := tree[r.URL.Query().Get("parent")]
		if !ok {
			t.Errorf("listed the children of %q, which reports no subfolders", r.URL.Query().Get("parent"))
		}
		return testutil.Response{Body: map[string]any{"data": children}}
	})
	srv.Handle("/ajax/account", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": 0, "trash_fullname": "", "spam_fullname": "", "drafts_fullname": "", "sent_fullname": "",
		"trash": "Trash", "spam": "Junk", "drafts": "Drafts", "sent": "Sent",
	}}})
	rows := func(r *http.Request) testutil.Response {
		columns := strings.Split(r.URL.Query().Get("columns"), ",")
		var data []any
		for _, message := range scopeMailbox[r.URL.Query().Get("folder")] {
			var from any = []any{[]any{message.fromName, message.from}}
			if message.fromName == "" {
				from = []any{[]any{nil, message.from}}
			}
			row := []any{message.id, message.subject, from, message.date}
			if slices.Contains(columns, "604") {
				to := []any{}
				if message.to != "" {
					to = []any{[]any{nil, message.to}}
				}
				row = append(row, to)
			}
			if slices.Contains(columns, "605") {
				row = append(row, []any{})
			}
			data = append(data, row)
		}
		return testutil.Response{Body: map[string]any{"data": data}}
	}
	srv.HandleFunc("/ajax/mail", "search", rows)
	srv.HandleFunc("/ajax/mail", "all", rows)
	srv.HandleFunc("/ajax/mail", "get", func(r *http.Request) testutil.Response {
		folder, id := r.URL.Query().Get("folder"), r.URL.Query().Get("id")
		for _, message := range scopeMailbox[folder] {
			if message.id == id {
				return testutil.Response{Body: map[string]any{"data": map[string]any{
					"id": id, "folder_id": folder, "subject": message.subject, "flags": 0,
					"from":        []any{[]any{message.fromName, message.from}},
					"attachments": []any{map[string]any{"id": "1", "content_type": "text/plain", "content": message.body}},
				}}}
			}
		}
		return testutil.Response{Body: map[string]any{"error": "not found", "code": "MSG-0032"}}
	})
	return srv
}

func requestedFolders(srv *testutil.Server, action string) []string {
	var folders []string
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/mail" && req.URL.Query().Get("action") == action {
			folders = append(folders, req.URL.Query().Get("folder"))
		}
	}
	return folders
}

func decodeMailListDocument(t *testing.T, stdout string) mailListDocument {
	t.Helper()
	var document mailListDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("stdout is not a mail list document: %v\n%s", err, stdout)
	}
	return document
}

func itemRefs(document mailListDocument) string {
	var refs []string
	for _, item := range document.Items {
		refs = append(refs, item.Folder+"#"+item.ID)
	}
	return strings.Join(refs, " ")
}

func TestMailSearch_DefaultScopeIsEveryFolderExceptTrashSpamAndDrafts(t *testing.T) {
	srv := newScopeServer(t)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr)
	}
	if got := strings.Join(requestedFolders(srv, "search"), ","); got != "default0/INBOX,default0/Sent,"+scopeLegal+",default0/INBOX/Kit" {
		t.Errorf("searched folders = %s, want every mail folder except Trash, Junk, Drafts and their subfolders", got)
	}
	document := decodeMailListDocument(t, stdout)
	// Equal dates sort by folder, so the order is fixed.
	if got := itemRefs(document); got != "default0/INBOX#1 default0/INBOX/Kit#1 "+scopeLegal+"#2 default0/Sent#1" {
		t.Errorf("items = %s, want newest first across the folders", got)
	}
	if document.HasMore || document.NextCursor != "" || document.Limit != 20 {
		t.Errorf("stdout = %s, want one complete page", stdout)
	}
	if !strings.Contains(stdout, `"folder":"`+scopeLegal+`"`) {
		t.Errorf("stdout = %s, want the folder id with a literal &", stdout)
	}
	inbox, sent := document.Items[0], document.Items[3]
	if inbox.FromName != "Sam Taylor" || inbox.To != nil || document.Items[1].FromName != "" {
		t.Errorf("inbox items = %+v, want from_name and no to", document.Items[:2])
	}
	if len(sent.To) != 1 || sent.To[0].Address != "support@supplier.example" {
		t.Errorf("sent item = %+v, want its recipient", sent)
	}
}

func TestMailSearch_FolderAllEqualsTheDefaultAndOneKeepsEverything(t *testing.T) {
	srv := newScopeServer(t)
	defer srv.Close()
	if _, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--folder", "ALL", "--llm"}); code != 0 {
		t.Fatalf("folder ALL: exit %d; stderr=%s", code, stderr)
	}
	all := requestedFolders(srv, "search")
	before := len(srv.Requests())
	if _, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--folder", "1", "--recursive", "--llm"}); code != 0 {
		t.Fatalf("folder 1: exit %d; stderr=%s", code, stderr)
	}
	var everything []string
	for _, req := range srv.Requests()[before:] {
		if req.URL.Query().Get("action") == "search" {
			everything = append(everything, req.URL.Query().Get("folder"))
		}
		if req.URL.Path == "/ajax/account" {
			t.Errorf("folder 1 without skip-trash read the account")
		}
	}
	if len(all) != 4 || len(everything) != 8 {
		t.Errorf("folder all searched %v, folder 1 searched %v; want 4 and every one of the 8 mail folders", all, everything)
	}
}

func TestMailSearch_RecursiveCursorAndOffsetContinueTheMergedList(t *testing.T) {
	srv := newScopeServer(t)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--limit", "2", "--llm"})
	if code != 0 {
		t.Fatalf("first page: exit %d; stderr=%s", code, stderr)
	}
	first := decodeMailListDocument(t, stdout)
	if itemRefs(first) != "default0/INBOX#1 default0/INBOX/Kit#1" || !first.HasMore || first.Available != 4 || !strings.HasPrefix(first.NextCursor, "at:") {
		t.Fatalf("first page = %s, want two items, has_more and a cursor", stdout)
	}
	stdout, stderr, code = run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--limit", "2", "--cursor", first.NextCursor, "--llm"})
	if code != 0 {
		t.Fatalf("second page: exit %d; stderr=%s", code, stderr)
	}
	second := decodeMailListDocument(t, stdout)
	if itemRefs(second) != scopeLegal+"#2 default0/Sent#1" || second.HasMore || second.NextCursor != "" {
		t.Errorf("second page = %s, want the other two items and no more", stdout)
	}
	stdout, _, code = run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--offset", "3", "--llm"})
	if code != 0 || itemRefs(decodeMailListDocument(t, stdout)) != "default0/Sent#1" {
		t.Errorf("offset 3: code %d, stdout = %s, want the last item", code, stdout)
	}
	_, stderr, code = run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--folder", "default0/INBOX", "--cursor", first.NextCursor, "--llm"})
	if code != 4 || !strings.Contains(stderr, "--recursive") {
		t.Errorf("recursive cursor for one folder: code %d, stderr = %s", code, stderr)
	}
	_, stderr, code = run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--cursor", "offset:2", "--llm"})
	if code != 4 || !strings.Contains(stderr, "one folder") {
		t.Errorf("offset cursor for all folders: code %d, stderr = %s", code, stderr)
	}
}

func TestMailSearch_FitInARecursiveListGivesACursorThatResumes(t *testing.T) {
	srv := newScopeServer(t)
	defer srv.Close()

	var seen []string
	cursor := ""
	for page := 0; page < 5; page++ {
		args := []string{"mail", "search", "--query", "kit", "--fit", "--max-output-bytes", "520", "--llm"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		stdout, stderr, code := run(t, srv, mailSearchEnv, args)
		if code != 0 {
			t.Fatalf("page %d: exit %d; stderr=%s", page, code, stderr)
		}
		if len(stdout) > 520 {
			t.Errorf("page %d is %d bytes, over the limit", page, len(stdout))
		}
		document := decodeMailListDocument(t, stdout)
		if document.Count == 0 {
			t.Fatalf("page %d holds nothing: %s", page, stdout)
		}
		seen = append(seen, itemRefs(document))
		if !document.HasMore {
			break
		}
		if !strings.HasPrefix(document.NextCursor, "at:") {
			t.Fatalf("page %d = %s, want a cursor", page, stdout)
		}
		cursor = document.NextCursor
	}
	if got := strings.Join(seen, " "); got != "default0/INBOX#1 default0/INBOX/Kit#1 "+scopeLegal+"#2 default0/Sent#1" || len(seen) < 2 {
		t.Errorf("pages = %q, want every item once over more than one page", seen)
	}
}

func TestMailSearch_SnippetReadsTheStartOfEachBody(t *testing.T) {
	srv := newScopeServer(t)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "search", "--query", "kit", "--limit", "1", "--fields", "snippet", "--llm"})
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr)
	}
	document := decodeMailListDocument(t, stdout)
	if len(document.Items) != 1 || document.Items[0].Snippet == nil || *document.Items[0].Snippet != "Hello Robin, the offer is attached." {
		t.Errorf("stdout = %s, want the collapsed start of the body", stdout)
	}
	gets := 0
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/mail" && req.URL.Query().Get("action") == "get" {
			gets++
			if req.URL.Query().Get("unseen") != "true" {
				t.Errorf("snippet read %s without unseen=true", req.URL)
			}
		}
	}
	if gets != 1 {
		t.Errorf("mail get requests = %d, want one per returned item", gets)
	}
	for _, args := range [][]string{
		{"mail", "search", "--query", "kit", "--limit", "51", "--fields", "snippet", "--llm"},
		{"mail", "search", "--query", "kit", "--all", "--fields", "snippet", "--llm"},
	} {
		if _, stderr, code := run(t, srv, mailSearchEnv, args); code != 4 || !strings.Contains(stderr, "--limit 50") {
			t.Errorf("%v: code %d, stderr = %s", args, code, stderr)
		}
	}
	if got := mailSnippet(strings.Repeat("ab ", 150)); len([]rune(got)) != mailSnippetChars+1 || !strings.HasSuffix(got, "…") {
		t.Errorf("long snippet = %q, want %d characters and an ellipsis", got, mailSnippetChars)
	}
}

func TestMailList_SentFolderIncludesToWithoutFields(t *testing.T) {
	srv := newScopeServer(t)
	defer srv.Close()

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "list", "--folder", "default0/Sent", "--llm"})
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, `"to":[{"address":"support@supplier.example"}]`) {
		t.Errorf("stdout = %s, want the recipient of the sent message", stdout)
	}
	stdout, _, code = run(t, srv, mailSearchEnv, []string{"mail", "list", "--folder", "default0/INBOX", "--llm"})
	if code != 0 || strings.Contains(stdout, `"to"`) {
		t.Errorf("inbox: code %d, stdout = %s, want no to", code, stdout)
	}
}

func TestMailListKey_OrdersNewestThenFolderThenHighestID(t *testing.T) {
	keys := []mailListKey{
		{Date: "2026-01-02T00:00:00Z", Folder: "b", ID: "2"},
		{Date: "2026-01-02T00:00:00Z", Folder: "a", ID: "2"},
		{Date: "2026-01-02T00:00:00Z", Folder: "a", ID: "10"},
		{Date: "2026-01-03T00:00:00Z", Folder: "z", ID: "1"},
	}
	slices.SortFunc(keys, func(a, b mailListKey) int {
		if a.before(b) {
			return -1
		}
		if b.before(a) {
			return 1
		}
		return 0
	})
	got := fmt.Sprint(keys)
	if got != "[{2026-01-03T00:00:00Z z 1} {2026-01-02T00:00:00Z a 10} {2026-01-02T00:00:00Z a 2} {2026-01-02T00:00:00Z b 2}]" {
		t.Errorf("order = %s", got)
	}
	options := mailListOptions{recursive: true, cursor: keys[1].cursor()}
	if cerr := parseMailListCursor(&options); cerr != nil || *options.startKey != keys[1] {
		t.Errorf("cursor round trip = %+v, %v", options.startKey, cerr)
	}
	for _, cursor := range []string{"at:%%%", "at:" + "WyJ4Il0", "page:2", "offset:-1"} {
		options := mailListOptions{recursive: !strings.HasPrefix(cursor, "offset"), cursor: cursor}
		if cerr := parseMailListCursor(&options); cerr == nil {
			t.Errorf("cursor %q was accepted", cursor)
		}
	}
}
