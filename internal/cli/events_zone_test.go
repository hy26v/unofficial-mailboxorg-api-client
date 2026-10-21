// ABOUTME: Tests for events time zones, all-day updates, and recurring series handling:
// ABOUTME: list expansion, --timezone, --recurrence-id, and the --series delete guard.
package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
)

var eventsTestEnv = map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}

func eventsZoneTestServer() *testutil.Server {
	srv := testutil.NewServer()
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	return srv
}

// chronosRequest returns the last request for the chronos action, or nil.
func chronosRequest(srv *testutil.Server, action string) *http.Request {
	var found *http.Request
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/chronos" && r.URL.Query().Get("action") == action {
			found = r
		}
	}
	return found
}

func decodeRequestBody(t *testing.T, r *http.Request, into any) {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	if err := json.Unmarshal(body, into); err != nil {
		t.Fatalf("request body is not JSON: %s", body)
	}
}

// TestEventsList_ExpandsRecurringSeriesByDefault is the regression test for hidden
// occurrences: chronos?action=all without expand returned a series once.
func TestEventsList_ExpandsRecurringSeriesByDefault(t *testing.T) {
	for _, tc := range []struct {
		name  string
		extra []string
		want  string
	}{
		{"default", nil, "true"},
		{"no-expand", []string{"--no-expand"}, "false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := eventsZoneTestServer()
			defer srv.Close()
			srv.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": []any{}}})
			args := append([]string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z", "--llm"}, tc.extra...)
			_, stderr, code := run(t, srv, eventsTestEnv, args)
			if code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
			}
			r := chronosRequest(srv, "all")
			if r == nil {
				t.Fatal("no chronos all request observed")
			}
			if got := r.URL.Query().Get("expand"); got != tc.want {
				t.Errorf("expand = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEventsCreate_UsesTheAccountTimeZone is the regression test for create sending every
// time as UTC, which moves recurring events by an hour after a daylight saving change.
func TestEventsCreate_UsesTheAccountTimeZone(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/config/timezone", "", testutil.Response{Body: map[string]any{"data": "Europe/Berlin", "timestamp": 1}})
	srv.Handle("/ajax/chronos", "new", testutil.Response{Body: map[string]any{"data": map[string]any{
		"created": []any{map[string]any{"id": "evt-new"}},
	}}})

	_, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "create", "--summary", "Standup", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T09:30:00Z", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	r := chronosRequest(srv, "new")
	if r == nil {
		t.Fatal("no create request observed")
	}
	var body map[string]any
	decodeRequestBody(t, r, &body)
	if want := map[string]any{"value": "20260701T110000", "tzid": "Europe/Berlin"}; !reflect.DeepEqual(body["startDate"], want) {
		t.Errorf("startDate = %#v, want %#v", body["startDate"], want)
	}
	if want := map[string]any{"value": "20260701T113000", "tzid": "Europe/Berlin"}; !reflect.DeepEqual(body["endDate"], want) {
		t.Errorf("endDate = %#v, want %#v", body["endDate"], want)
	}
}

func TestEventsCreate_TimezoneFlagWinsOverTheAccountZone(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/config/timezone", "", testutil.Response{Body: map[string]any{"data": "Europe/Berlin", "timestamp": 1}})
	srv.Handle("/ajax/chronos", "new", testutil.Response{Body: map[string]any{"data": map[string]any{
		"created": []any{map[string]any{"id": "evt-new"}},
	}}})

	_, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "create", "--summary", "Standup", "--start", "2026-01-15T14:00:00Z", "--end", "2026-01-15T15:00:00Z",
		"--timezone", "America/New_York", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	for _, r := range srv.Requests() {
		if r.URL.Path == "/ajax/config/timezone" {
			t.Error("read the account time zone although --timezone was given")
		}
	}
	var body map[string]any
	decodeRequestBody(t, chronosRequest(srv, "new"), &body)
	if want := map[string]any{"value": "20260115T090000", "tzid": "America/New_York"}; !reflect.DeepEqual(body["startDate"], want) {
		t.Errorf("startDate = %#v, want %#v", body["startDate"], want)
	}
}

func TestEventsCreate_RejectsUnknownTimezoneBeforeAnyRequest(t *testing.T) {
	for _, zone := range []string{"Mars/Olympus", "Local", ""} {
		t.Run(zone, func(t *testing.T) {
			srv := eventsZoneTestServer()
			defer srv.Close()
			_, stderr, code := run(t, srv, eventsTestEnv, []string{
				"events", "create", "--summary", "S", "--start", "2026-07-01T09:00:00Z", "--end", "2026-07-01T10:00:00Z",
				"--timezone", zone, "--llm",
			})
			if code != 4 || !strings.Contains(stderr, "--timezone") {
				t.Fatalf("exit code = %d, stderr = %q; want invalid_argument about --timezone", code, stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

// TestEventsUpdate_KeepsTheEventTimeZone is the regression test for update overwriting the
// event's zone with UTC.
func TestEventsUpdate_KeepsTheEventTimeZone(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id":        "evt-1",
		"startDate": map[string]any{"value": "20260701T110000", "tzid": "Europe/Berlin"},
		"endDate":   map[string]any{"value": "20260701T120000", "tzid": "Europe/Berlin"},
	}, "timestamp": 1000}})
	srv.Handle("/ajax/chronos", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": "evt-1"}}})

	_, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "update", "--id", "evt-1", "--start", "2026-07-01T10:00:00Z", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var body map[string]any
	decodeRequestBody(t, chronosRequest(srv, "update"), &body)
	want := map[string]any{"startDate": map[string]any{"value": "20260701T120000", "tzid": "Europe/Berlin"}}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %#v, want %#v", body, want)
	}
}

func TestEventsUpdate_TimezoneFlagReplacesTheZone(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id":        "evt-1",
		"startDate": map[string]any{"value": "20260701T110000", "tzid": "Europe/Berlin"},
	}, "timestamp": 1000}})
	srv.Handle("/ajax/chronos", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": "evt-1"}}})

	_, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "update", "--id", "evt-1", "--start", "2026-07-01T10:00:00Z", "--end", "2026-07-01T11:00:00Z",
		"--timezone", "UTC", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var body map[string]any
	decodeRequestBody(t, chronosRequest(srv, "update"), &body)
	want := map[string]any{
		"startDate": map[string]any{"value": "20260701T100000Z", "tzid": "UTC"},
		"endDate":   map[string]any{"value": "20260701T110000Z", "tzid": "UTC"},
	}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %#v, want %#v", body, want)
	}
}

// An all-day event uses date-only values. Changing only --start used to send a timed UTC
// start next to the date-only end.
func TestEventsUpdate_AllDayStartStaysDateOnly(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id":        "evt-1",
		"startDate": map[string]any{"value": "20260701"},
		"endDate":   map[string]any{"value": "20260703"},
	}, "timestamp": 1000}})
	srv.Handle("/ajax/chronos", "update", testutil.Response{Body: map[string]any{"data": map[string]any{"id": "evt-1"}}})

	_, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "update", "--id", "evt-1", "--start", "2026-07-02T00:00:00+02:00", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	var body map[string]any
	decodeRequestBody(t, chronosRequest(srv, "update"), &body)
	want := map[string]any{"startDate": map[string]any{"value": "20260702"}}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("body = %#v, want %#v", body, want)
	}
}

func TestEventsUpdate_AllDayStartAfterEndIsRefused(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id":        "evt-1",
		"startDate": map[string]any{"value": "20260701"},
		"endDate":   map[string]any{"value": "20260702"},
	}, "timestamp": 1000}})

	_, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "update", "--id", "evt-1", "--start", "2026-07-05T00:00:00Z", "--llm",
	})
	if code != 4 || !strings.Contains(stderr, "all-day") {
		t.Fatalf("exit code = %d, stderr = %q; want invalid_argument about the all-day end", code, stderr)
	}
	if chronosRequest(srv, "update") != nil {
		t.Error("sent an update for an all-day start after the end")
	}
}

func TestEventsUpdate_RecurrenceIDTargetsOneOccurrence(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "evt-1", "seriesId": "evt-1", "recurrenceId": "20260708T090000Z",
	}, "timestamp": 1000}})
	// Updating one occurrence creates a change exception with its own id.
	srv.Handle("/ajax/chronos", "update", testutil.Response{Body: map[string]any{"data": map[string]any{
		"created": []any{map[string]any{"id": "evt-9", "seriesId": "evt-1"}},
		"updated": []any{},
	}}})

	stdout, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "update", "--id", "evt-1", "--recurrence-id", "20260708T090000Z", "--summary", "Moved", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"id":"evt-1","recurrence_id":"20260708T090000Z"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
	for _, action := range []string{"get", "update"} {
		if got := chronosRequest(srv, action).URL.Query().Get("recurrenceId"); got != "20260708T090000Z" {
			t.Errorf("%s recurrenceId = %q, want 20260708T090000Z", action, got)
		}
	}
}

func TestEventsUpdateAndDelete_RejectMalformedRecurrenceID(t *testing.T) {
	for _, args := range [][]string{
		{"events", "update", "--id", "evt-1", "--summary", "S", "--recurrence-id", "next tuesday"},
		{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--recurrence-id", "Mars/Olympus:20260708T090000"},
		{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--recurrence-id", "20260708", "--series"},
	} {
		t.Run(strings.Join(args[len(args)-2:], " "), func(t *testing.T) {
			srv := eventsZoneTestServer()
			defer srv.Close()
			_, stderr, code := run(t, srv, eventsTestEnv, append(args, "--llm"))
			if code != 4 || !strings.Contains(stderr, "--recurrence-id") {
				t.Fatalf("exit code = %d, stderr = %q; want invalid_argument about --recurrence-id", code, stderr)
			}
			if len(srv.Requests()) != 0 {
				t.Errorf("made %d requests, want 0", len(srv.Requests()))
			}
		})
	}
}

// seriesMasterServer answers chronos get with a recurring series master.
func seriesMasterServer() *testutil.Server {
	srv := eventsZoneTestServer()
	srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "evt-1", "seriesId": "evt-1", "rrule": "FREQ=WEEKLY",
	}, "timestamp": 2000}})
	return srv
}

// TestEventsDelete_SeriesMasterNeedsSeriesFlag is the regression test for deleting a whole
// recurring series by accident: events list shows occurrences with the series id.
func TestEventsDelete_SeriesMasterNeedsSeriesFlag(t *testing.T) {
	srv := seriesMasterServer()
	defer srv.Close()

	stdout, stderr, code := run(t, srv, eventsTestEnv, []string{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--llm"})
	if code != 4 {
		t.Fatalf("exit code = %d, want 4; stdout=%q", code, stdout)
	}
	if !strings.Contains(stderr, "--series") || !strings.Contains(stderr, "--recurrence-id") {
		t.Errorf("stderr = %q, want both --series and --recurrence-id named", stderr)
	}
	if chronosRequest(srv, "delete") != nil {
		t.Error("sent a delete for a series master without --series")
	}
}

func TestEventsDelete_SeriesFlagDeletesTheSeries(t *testing.T) {
	srv := seriesMasterServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "delete", testutil.Response{Body: map[string]any{"data": []any{map[string]any{
		"folderId": "cal://0/32", "id": "evt-1",
		"deleted": []any{map[string]any{"id": "evt-1", "folder": "cal://0/32"}},
	}}}})

	stdout, stderr, code := run(t, srv, eventsTestEnv, []string{"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--series", "--llm"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"deleted":true,"id":"evt-1"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestEventsDelete_RecurrenceIDDeletesOneOccurrence(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"id": "evt-1", "seriesId": "evt-1", "recurrenceId": "20260708T090000Z",
	}, "timestamp": 2000}})
	// The series stays; its master is updated with a delete exception.
	srv.Handle("/ajax/chronos", "delete", testutil.Response{Body: map[string]any{"data": []any{map[string]any{
		"folderId": "cal://0/32", "id": "evt-1",
		"updated": []any{map[string]any{"id": "evt-1", "folder": "cal://0/32"}},
	}}}})

	stdout, stderr, code := run(t, srv, eventsTestEnv, []string{
		"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--recurrence-id", "20260708T090000Z", "--llm",
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, stderr)
	}
	if stdout != `{"deleted":true,"id":"evt-1","recurrence_id":"20260708T090000Z"}`+"\n" {
		t.Errorf("stdout = %q", stdout)
	}
	var body []map[string]any
	decodeRequestBody(t, chronosRequest(srv, "delete"), &body)
	want := []map[string]any{{"id": "evt-1", "folder": "cal://0/32", "recurrenceId": "20260708T090000Z"}}
	if !reflect.DeepEqual(body, want) {
		t.Errorf("delete body = %#v, want %#v", body, want)
	}
}

// An occurrence that already has a change exception can come back from chronos?action=get
// as that exception, with its own id and the series id as seriesId. Update and delete
// accept it and still address the occurrence by the series id plus recurrenceId.
func TestEvents_RecurrenceIDAcceptsAnExistingChangeException(t *testing.T) {
	exception := map[string]any{"data": map[string]any{
		"id": "evt-9", "seriesId": "evt-1", "recurrenceId": "Europe/Berlin:20260708T110000",
	}, "timestamp": 3000}

	t.Run("update", func(t *testing.T) {
		srv := eventsZoneTestServer()
		defer srv.Close()
		srv.Handle("/ajax/chronos", "get", testutil.Response{Body: exception})
		srv.Handle("/ajax/chronos", "update", testutil.Response{Body: map[string]any{"data": map[string]any{
			"updated": []any{map[string]any{"id": "evt-9", "seriesId": "evt-1"}},
		}}})
		stdout, stderr, code := run(t, srv, eventsTestEnv, []string{
			"events", "update", "--id", "evt-1", "--recurrence-id", "20260708T090000Z", "--summary", "Moved", "--llm",
		})
		if code != 0 || stdout != `{"id":"evt-1","recurrence_id":"20260708T090000Z"}`+"\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%s", code, stdout, stderr)
		}
		query := chronosRequest(srv, "update").URL.Query()
		if query.Get("id") != "evt-1" || query.Get("recurrenceId") != "20260708T090000Z" || query.Get("timestamp") != "3000" {
			t.Errorf("update query = %s, want the series id, the recurrence id and the exception's timestamp", query.Encode())
		}
	})

	t.Run("delete", func(t *testing.T) {
		srv := eventsZoneTestServer()
		defer srv.Close()
		srv.Handle("/ajax/chronos", "get", testutil.Response{Body: exception})
		srv.Handle("/ajax/chronos", "delete", testutil.Response{Body: map[string]any{"data": []any{map[string]any{
			"folderId": "cal://0/32", "id": "evt-1",
			"deleted": []any{map[string]any{"id": "evt-9", "folder": "cal://0/32"}},
		}}}})
		stdout, stderr, code := run(t, srv, eventsTestEnv, []string{
			"events", "delete", "--id", "evt-1", "--confirm", "evt-1", "--recurrence-id", "20260708T090000Z", "--llm",
		})
		if code != 0 || stdout != `{"deleted":true,"id":"evt-1","recurrence_id":"20260708T090000Z"}`+"\n" {
			t.Fatalf("exit=%d stdout=%q stderr=%s", code, stdout, stderr)
		}
		var body []map[string]any
		decodeRequestBody(t, chronosRequest(srv, "delete"), &body)
		want := []map[string]any{{"id": "evt-1", "folder": "cal://0/32", "recurrenceId": "20260708T090000Z"}}
		if !reflect.DeepEqual(body, want) {
			t.Errorf("delete body = %#v, want %#v", body, want)
		}
	})
}

// A different event, or an exception for another occurrence, is still refused before any
// change.
func TestEvents_RecurrenceIDRejectsAnUnrelatedEvent(t *testing.T) {
	for name, data := range map[string]map[string]any{
		"other series":     {"id": "evt-9", "seriesId": "evt-2", "recurrenceId": "20260708T090000Z"},
		"other occurrence": {"id": "evt-9", "seriesId": "evt-1", "recurrenceId": "20260715T090000Z"},
		"no series id":     {"id": "evt-9", "recurrenceId": "20260708T090000Z"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := eventsZoneTestServer()
			defer srv.Close()
			srv.Handle("/ajax/chronos", "get", testutil.Response{Body: map[string]any{"data": data, "timestamp": 3000}})
			_, stderr, code := run(t, srv, eventsTestEnv, []string{
				"events", "update", "--id", "evt-1", "--recurrence-id", "20260708T090000Z", "--summary", "Moved", "--llm",
			})
			if code == 0 || !strings.Contains(stderr, "invalid_response") {
				t.Fatalf("exit=%d stderr=%s, want invalid_response", code, stderr)
			}
			if chronosRequest(srv, "update") != nil {
				t.Fatal("sent an update for an unrelated event")
			}
		})
	}
}

func TestRecurrenceIDsMatch(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"20260708T090000Z", "20260708T090000Z", true},
		{"Europe/Berlin:20260708T110000", "20260708T090000Z", true},
		{"Europe/Berlin:20260708T090000", "20260708T090000Z", false},
		{"20260708", "20260708", true},
		{"20260708T090000", "20260708T090000Z", false},
		{"", "", false},
	} {
		if got := recurrenceIDsMatch(tc.a, tc.b); got != tc.want {
			t.Errorf("recurrenceIDsMatch(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// Expanded occurrences share the series id, so human events list shows the recurrence id,
// and a local time is shown with its zone unless that zone is UTC.
func TestEventsList_HumanShowsRecurrenceIDAndTimeZone(t *testing.T) {
	srv := eventsZoneTestServer()
	defer srv.Close()
	srv.Handle("/ajax/chronos", "all", testutil.Response{Body: map[string]any{"data": []any{
		map[string]any{"id": "evt-1", "seriesId": "evt-1", "recurrenceId": "20260708T090000Z", "summary": "Standup",
			"startDate": map[string]any{"value": "20260708T110000", "tzid": "Europe/Berlin"},
			"endDate":   map[string]any{"value": "20260708T113000", "tzid": "Europe/Berlin"}},
		map[string]any{"id": "evt-1", "seriesId": "evt-1", "recurrenceId": "20260715T090000Z", "summary": "Standup",
			"startDate": map[string]any{"value": "20260715T110000", "tzid": "Europe/Berlin"},
			"endDate":   map[string]any{"value": "20260715T113000", "tzid": "Europe/Berlin"}},
		map[string]any{"id": "evt-2", "summary": "Call",
			"startDate": map[string]any{"value": "20260709T090000Z", "tzid": "UTC"},
			"endDate":   map[string]any{"value": "20260709T100000", "tzid": "Etc/UTC"}},
	}}})

	stdout, stderr, code := run(t, srv, eventsTestEnv, []string{"events", "list", "--from", "2026-07-01T00:00:00Z", "--to", "2026-08-01T00:00:00Z"})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr)
	}
	lines := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(lines) != 4 || strings.Join(strings.Fields(lines[0]), " ") != "id recurrence_id summary start end" {
		t.Fatalf("stdout = %q, want a header with recurrence_id and three rows", stdout)
	}
	for i, want := range []string{
		"evt-1 20260708T090000Z Standup 20260708T110000 Europe/Berlin 20260708T113000 Europe/Berlin",
		"evt-1 20260715T090000Z Standup 20260715T110000 Europe/Berlin 20260715T113000 Europe/Berlin",
		"evt-2 Call 20260709T090000Z 20260709T100000",
	} {
		if got := strings.Join(strings.Fields(lines[i+1]), " "); got != want {
			t.Errorf("row %d = %q, want %q", i, got, want)
		}
	}
}
