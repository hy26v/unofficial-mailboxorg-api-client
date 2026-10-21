// ABOUTME: Offline contract tests for mail range pagination, safe rich fields, filters, and recursion.
package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func richMailColumnValue(column, id, folder string) any {
	n, _ := strconv.Atoi(id)
	switch column {
	case "600":
		return id
	case "601":
		return folder
	case "602":
		return n%2 == 0
	case "603":
		address := "sender@example.org"
		if n%2 == 0 {
			address = "other@example.org"
		}
		return []any{[]any{"Sender", address}}
	case "604":
		domain := "example.net"
		if n%2 == 0 {
			domain = "elsewhere.test"
		}
		return []any{[]any{"Recipient", "person@" + domain}}
	case "605":
		return []any{[]any{nil, "copy@example.net"}}
	case "607":
		return "Subject " + id
	case "608":
		return 1000 + n
	case "609":
		return int64(1700000000000 + n*1000)
	case "610":
		return int64(1700000100000 + n*1000)
	case "611":
		return int64(mailFlagSeen | (n%2)*mailFlagFlagged)
	default:
		return nil
	}
}

func richMailRow(columns, id, folder string) []any {
	ids := strings.Split(columns, ",")
	row := make([]any, len(ids))
	for i, column := range ids {
		row[i] = richMailColumnValue(column, id, folder)
	}
	return row
}

func newPaginatedMailServer(t *testing.T, total int, includeFolderTree bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("action")
		switch r.URL.Path + "?" + action {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/mail?all":
			left, _ := strconv.Atoi(r.URL.Query().Get("left_hand_limit"))
			right, _ := strconv.Atoi(r.URL.Query().Get("right_hand_limit"))
			if right < left {
				t.Errorf("invalid range %d:%d", left, right)
			}
			if right > total {
				right = total
			}
			rows := make([]any, 0, right-left)
			folder := r.URL.Query().Get("folder")
			for index := left; index < right; index++ {
				rows = append(rows, richMailRow(r.URL.Query().Get("columns"), strconv.Itoa(index+1), folder))
			}
			writeJSONResponse(w, map[string]any{"data": rows})
		case "/ajax/folders?list":
			rows := []any{}
			if includeFolderTree {
				rows = append(rows, []any{"default0/INBOX/Child", "Child", "mail", 1, false})
			}
			writeJSONResponse(w, map[string]any{"data": rows})
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestMailList_OffsetCursorAndAllJSONL(t *testing.T) {
	srv := newPaginatedMailServer(t, 5, false)
	defer srv.Close()
	root := t.TempDir()
	stdout, stderr, code := runAgainstHTTPServer(t, srv, root, []string{"mail", "list", "--limit", "2", "--llm"})
	if code != 0 {
		t.Fatalf("first page exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{`"id":"1"`, `"id":"2"`, `"has_more":true`, `"next_offset":2`, `"next_cursor":"offset:2"`} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("first page=%s want %s", stdout, want)
		}
	}
	stdout, stderr, code = runAgainstHTTPServer(t, srv, root, []string{"mail", "list", "--limit", "2", "--format", "jsonl"})
	if code != 0 {
		t.Fatalf("paginated jsonl exit=%d stderr=%s", code, stderr)
	}
	if lines := strings.Split(strings.TrimSpace(stdout), "\n"); len(lines) != 2 {
		t.Fatalf("paginated jsonl lines=%d want 2: %s", len(lines), stdout)
	}
	stdout, stderr, code = runAgainstHTTPServer(t, srv, root, []string{"mail", "list", "--limit", "2", "--cursor", "offset:2", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"id":"3"`) || strings.Contains(stdout, `"id":"1"`) {
		t.Fatalf("cursor page exit=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	fields := "to,cc,size,sent_date,received_date,attachment,seen,flagged"
	stdout, stderr, code = runAgainstHTTPServer(t, srv, root, []string{"mail", "list", "--all", "--page-size", "2", "--fields", fields, "--format", "jsonl"})
	if code != 0 {
		t.Fatalf("all/jsonl exit=%d stderr=%s", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 5 {
		t.Fatalf("jsonl lines=%d want 5: %s", len(lines), stdout)
	}
	for index, line := range lines {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %d invalid JSON: %v", index, err)
		}
		for _, field := range []string{"to", "cc", "size", "sent_date", "received_date", "attachment", "seen", "flagged"} {
			if _, ok := row[field]; !ok {
				t.Fatalf("line %d lacks %s: %s", index, field, line)
			}
		}
	}
}

func TestMailList_MetadataFiltersAndRecursiveFolders(t *testing.T) {
	srv := newPaginatedMailServer(t, 5, false)
	root := t.TempDir()
	stdout, stderr, code := runAgainstHTTPServer(t, srv, root, []string{
		"mail", "list", "--all", "--page-size", "2", "--from", "sender@example.org", "--to-domain", "@example.net",
		"--since", "2023-11-14T00:00:00Z", "--until", "2023-11-15T00:00:00Z", "--llm",
	})
	srv.Close()
	if code != 0 {
		t.Fatalf("filter exit=%d stderr=%s", code, stderr)
	}
	for _, id := range []string{"1", "3", "5"} {
		if !strings.Contains(stdout, fmt.Sprintf(`"id":"%s"`, id)) {
			t.Fatalf("filtered output missing id %s: %s", id, stdout)
		}
	}
	for _, id := range []string{"2", "4"} {
		if strings.Contains(stdout, fmt.Sprintf(`"id":"%s"`, id)) {
			t.Fatalf("filtered output unexpectedly includes id %s: %s", id, stdout)
		}
	}

	recursiveServer := newPaginatedMailServer(t, 1, true)
	defer recursiveServer.Close()
	stdout, stderr, code = runAgainstHTTPServer(t, recursiveServer, t.TempDir(), []string{
		"mail", "list", "--folder", "default0/INBOX", "--all", "--recursive", "--fields", "folder", "--llm",
	})
	if code != 0 {
		t.Fatalf("recursive exit=%d stderr=%s", code, stderr)
	}
	for _, folder := range []string{"default0/INBOX", "default0/INBOX/Child"} {
		if !strings.Contains(stdout, `"folder":"`+folder+`"`) {
			t.Fatalf("recursive output missing folder %s: %s", folder, stdout)
		}
	}
}

func TestMailList_RecursiveFromPrivateRootSkipsTheRoot(t *testing.T) {
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/mail", "all", testutil.Response{Body: map[string]any{"data": []any{
		[]any{"7", "Hello", []any{[]any{nil, "sender@example.org"}}, int64(1784851574000)},
	}}})
	srv.Handle("/ajax/folders", "list", testutil.Response{Body: map[string]any{"data": []any{
		[]any{"default0/INBOX", "Inbox", "mail", 1, true},
		[]any{"default0/Sent", "Sent", "mail", 1, false},
		[]any{"33", "Contacts", "contacts", 1, false},
	}}})

	stdout, stderr, code := run(t, srv, mailSearchEnv, []string{"mail", "list", "--folder", "1", "--all", "--recursive", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	listed := []string{}
	for _, req := range srv.Requests() {
		if req.URL.Path == "/ajax/mail" {
			listed = append(listed, req.URL.Query().Get("folder"))
		}
	}
	if strings.Join(listed, ",") != "default0/INBOX,default0/Sent" {
		t.Errorf("listed folders = %v, want only the mail folders below the private root", listed)
	}
	var document mailListDocument
	if err := json.Unmarshal([]byte(stdout), &document); err != nil || document.Count != 2 {
		t.Errorf("stdout = %s, want one message per mail folder", stdout)
	}
}
