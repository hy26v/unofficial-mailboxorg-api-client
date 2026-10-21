// ABOUTME: Offline tests that every mail request decoding sent_date or received_date asks OX for UTC.
// ABOUTME: A fake server shifts dates by a user offset unless timezone=UTC is sent, like OX does.
package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const (
	// mailDatesTrueUTC is 2026-07-24T00:06:14Z in epoch milliseconds.
	mailDatesTrueUTC = int64(1784851574000)
	// mailDatesUserOffset is the +02:00 offset OX adds when timezone is not sent.
	mailDatesUserOffset = int64(2 * 60 * 60 * 1000)
)

// newMailDatesServer imitates the OX date rule: Time values carry the user's timezone
// offset unless the request has timezone=UTC. It records every mail request query.
func newMailDatesServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	mailQueries := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		action := query.Get("action")
		date := mailDatesTrueUTC
		if query.Get("timezone") != "UTC" {
			date += mailDatesUserOffset
		}
		if r.URL.Path == "/ajax/mail" {
			mu.Lock()
			mailQueries = append(mailQueries, action+" "+r.URL.RawQuery)
			mu.Unlock()
		}
		switch r.URL.Path + "?" + action {
		case "/ajax/login?login", "/ajax/login?autologin":
			writeJSONResponse(w, map[string]any{"session": "sess-1"})
		case "/ajax/mail?all", "/ajax/mail?search":
			writeJSONResponse(w, map[string]any{"data": []any{
				[]any{"7", "Dated", []any{[]any{nil, "sender@example.org"}}, date},
			}})
		case "/ajax/mail?list":
			var requested []map[string]string
			_ = json.Unmarshal([]byte(query.Get("data")), &requested)
			if len(requested) != 1 {
				writeJSONResponse(w, map[string]any{"data": []any{}})
				return
			}
			row := safeMailRow(requested[0]["id"], requested[0]["folder"])
			row[7], row[8] = date, date
			writeJSONResponse(w, map[string]any{"data": []any{row}})
		case "/ajax/mail?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": "7", "folder_id": "default0/INBOX", "subject": "Dated",
				"from": [][]any{{nil, "sender@example.org"}}, "sent_date": date, "received_date": date,
				"flags": 0, "attachments": []any{},
			}})
		case "/ajax/mail?update":
			writeJSONResponse(w, map[string]any{"data": map[string]any{"folder_id": "default0/Archive", "id": "17"}})
		case "/ajax/folders?get":
			writeJSONResponse(w, map[string]any{"data": map[string]any{
				"id": "default0/Archive", "title": "Archive", "module": "mail", "folder_id": "default0", "subfolders": false,
				"last_modified": nil, "permissions": []any{},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), mailQueries...)
	}
}

func TestMailDates_EveryDateRequestAsksForUTC(t *testing.T) {
	for name, args := range map[string][]string{
		"list":      {"mail", "list", "--llm"},
		"search":    {"mail", "search", "--query", "dated", "--llm"},
		"get":       {"mail", "get", "--id", "7", "--folder", "default0/INBOX", "--llm"},
		"read":      {"mail", "read", "--id", "7", "--folder", "default0/INBOX", "--llm"},
		"move":      {"mail", "move", "--id", "7", "--folder", "default0/INBOX", "--to", "default0/Archive", "--verify", "--llm"},
		"move-many": {"mail", "move-many", "--input", `[{"folder":"default0/INBOX","id":"7","to":"default0/Archive"}]`, "--verify", "--checkpoint", "", "--llm"},
	} {
		t.Run(name, func(t *testing.T) {
			srv, queries := newMailDatesServer(t)
			defer srv.Close()
			root := t.TempDir()
			for i := range args {
				if args[i] == "--checkpoint" {
					args[i+1] = root + "/moves.jsonl"
				}
			}
			stdout, stderr, code := runAgainstHTTPServer(t, srv, root, args)
			if code != 0 {
				t.Fatalf("exit=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			// Move output carries no dates; its fingerprints are checked by the query test below.
			showsDates := !strings.HasPrefix(name, "move")
			if showsDates && (strings.Contains(stdout, "02:06:14Z") || !strings.Contains(stdout, "2026-07-24T00:06:14Z")) {
				t.Errorf("stdout = %s, want the true UTC date 2026-07-24T00:06:14Z", stdout)
			}
			dateRequests := 0
			for _, query := range queries() {
				action := strings.SplitN(query, " ", 2)[0]
				if action == "update" {
					continue
				}
				dateRequests++
				if !strings.Contains(query, "timezone=UTC") {
					t.Errorf("mail request %q did not send timezone=UTC", query)
				}
			}
			if dateRequests == 0 {
				t.Fatalf("no mail date request was made")
			}
		})
	}
}

func TestMailDates_SinceAndUntilCompareTrueUTC(t *testing.T) {
	srv, _ := newMailDatesServer(t)
	defer srv.Close()

	// The message arrived at 00:06:14Z, which is 02:06:14+02:00. A lower bound one second
	// later, written with an offset, must exclude it; one second earlier must include it.
	stdout, stderr, code := runAgainstHTTPServer(t, srv, t.TempDir(), []string{"mail", "list", "--since", "2026-07-24T02:06:15+02:00", "--llm"})
	if code != 0 || strings.Contains(stdout, `"id":"7"`) {
		t.Errorf("--since after arrival: exit=%d stdout=%s stderr=%s, want no match", code, stdout, stderr)
	}
	stdout, stderr, code = runAgainstHTTPServer(t, srv, t.TempDir(), []string{"mail", "list", "--since", "2026-07-24T00:06:13Z", "--until", "2026-07-24T00:06:15Z", "--llm"})
	if code != 0 || !strings.Contains(stdout, `"id":"7"`) || !strings.Contains(stdout, `"date":"2026-07-24T00:06:14Z"`) {
		t.Errorf("window around arrival: exit=%d stdout=%s stderr=%s, want the message at 00:06:14Z", code, stdout, stderr)
	}
}
