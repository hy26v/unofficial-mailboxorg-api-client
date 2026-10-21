// ABOUTME: Tests the default time range of events list and reminder list without --from or --to.
// ABOUTME: A fixed clock makes the requested range deterministic.
package cli

import (
	"strconv"
	"testing"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

func fixClock(t *testing.T, now time.Time) {
	t.Helper()
	saved := nowFunc
	nowFunc = func() time.Time { return now }
	t.Cleanup(func() { nowFunc = saved })
}

func TestEventsList_DefaultsToTheNext30Days(t *testing.T) {
	fixClock(t, time.Date(2026, 10, 20, 12, 30, 15, 900, time.UTC))
	for _, tt := range []struct {
		args       []string
		start, end string
	}{
		{nil, "20261020T123015Z", "20261119T123015Z"},
		{[]string{"--from", "2026-12-01T00:00:00Z"}, "20261201T000000Z", "20261231T000000Z"},
		{[]string{"--to", "2026-10-21T00:00:00Z"}, "20261020T123015Z", "20261021T000000Z"},
	} {
		srv := testutil.NewServer()
		srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
		srv.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": []any{}}})
		args := append([]string{"events", "list", "--llm"}, tt.args...)
		if _, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u@example.invalid", "MBX_PASSWORD": "pw"}, args); code != 0 {
			t.Fatalf("%v: exit %d, %s", tt.args, code, stderr)
		}
		var start, end string
		for _, r := range srv.Requests() {
			if r.URL.Path == "/ajax/chronos" {
				start, end = r.URL.Query().Get("rangeStart"), r.URL.Query().Get("rangeEnd")
			}
		}
		if start != tt.start || end != tt.end {
			t.Errorf("%v: range %s..%s, want %s..%s", tt.args, start, end, tt.start, tt.end)
		}
		srv.Close()
	}
}

func TestReminderList_DefaultsToTheNext30Days(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	fixClock(t, now)
	srv := testutil.NewServer()
	defer srv.Close()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/reminder", "range", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": "1", "module": 4, "target_id": "12", "folder": "35", "alarm": now.Add(-time.Hour).UnixMilli()},
		map[string]any{"id": "2", "module": 4, "target_id": "13", "folder": "35", "alarm": now.Add(time.Hour).UnixMilli()},
	}}})
	stdout, stderr, code := run(t, srv, map[string]string{"MBX_USERNAME": "u@example.invalid", "MBX_PASSWORD": "pw"}, []string{"reminder", "list", "--format", "values", "--select", "id"})
	if code != 0 || stdout != "2\n" {
		t.Fatalf("exit %d, stdout %q, stderr %s", code, stdout, stderr)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/reminder" {
			if got, want := r.URL.Query().Get("end"), strconv.FormatInt(now.Add(defaultRangeLength).UnixMilli(), 10); got != want {
				t.Errorf("end = %s, want %s", got, want)
			}
		}
	}
}
