// ABOUTME: Time zone, all-day and recurrence helpers for the chronos events commands.
// ABOUTME: Builds startDate/endDate DateTime values and reads the current state of an event.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
)

// chronosLocalFormat is an RFC 5545 local date-time without a zone suffix; chronos reads it
// in the zone named by the DateTime's tzid.
const chronosLocalFormat = "20060102T150405"

// chronosDateFormat is an RFC 5545 date value, used without tzid for all-day events.
const chronosDateFormat = "20060102"

// loadEventZone validates an IANA time zone name given with --timezone.
func loadEventZone(name string) (*time.Location, *cliErrors.Error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed == "Local" || trimmed != name {
		return nil, &cliErrors.Error{Code: "invalid_argument", Message: "--timezone must be an IANA time zone name, e.g. Europe/Berlin, got: " + name}
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, &cliErrors.Error{Code: "invalid_argument", Message: "--timezone is not a known IANA time zone name: " + name}
	}
	return loc, nil
}

// chronosDateTimeIn formats t as a chronos DateTime in loc. UTC keeps the absolute
// "...Z" form the CLI always sent; other zones send the local value with the zone as tzid,
// so recurring events keep their wall-clock time across daylight saving changes.
func chronosDateTimeIn(t time.Time, loc *time.Location) map[string]string {
	if loc == nil || loc == time.UTC || loc.String() == "UTC" {
		return map[string]string{"value": t.UTC().Format(chronosTimeFormat), "tzid": "UTC"}
	}
	return map[string]string{"value": t.In(loc).Format(chronosLocalFormat), "tzid": loc.String()}
}

// chronosDateIn formats t as a date-only value for an all-day event. Without a zone, the
// calendar date written in the RFC3339 value is used as is.
func chronosDateIn(t time.Time, loc *time.Location) map[string]string {
	if loc != nil {
		t = t.In(loc)
	}
	return map[string]string{"value": t.Format(chronosDateFormat)}
}

// configuredTimeZone reads the user's configured time zone from GET /config/timezone. It
// reports false when the read fails or the value is not a loadable IANA name.
func configuredTimeZone(ctx context.Context, c *client.Client, sess client.Session) (*time.Location, bool) {
	data, err := c.Do(ctx, http.MethodGet, "/config/timezone", "", sess, nil)
	if err != nil {
		return nil, false
	}
	var name string
	if err := json.Unmarshal(data, &name); err != nil {
		return nil, false
	}
	loc, cerr := loadEventZone(name)
	if cerr != nil {
		return nil, false
	}
	return loc, true
}

// chronosDateTime is the decoded form of a chronos startDate/endDate value.
type chronosDateTime struct {
	Value string
	Tzid  string
}

func decodeChronosDateTime(raw json.RawMessage) *chronosDateTime {
	if len(raw) == 0 {
		return nil
	}
	var object struct {
		Value string `json:"value"`
		Tzid  string `json:"tzid"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && object.Value != "" {
		return &chronosDateTime{Value: object.Value, Tzid: object.Tzid}
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil && value != "" {
		return &chronosDateTime{Value: value}
	}
	return nil
}

// isDate reports whether the value is date-only (an all-day event boundary).
func (d *chronosDateTime) isDate() bool {
	return d != nil && d.Tzid == "" && len(d.Value) == len(chronosDateFormat)
}

// location returns the zone the value is stored in. A missing or UTC zone maps to UTC, the
// zone the CLI used before it kept event zones.
func (d *chronosDateTime) location(which string) (*time.Location, *cliErrors.Error) {
	if d == nil || d.Tzid == "" || d.Tzid == "UTC" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(d.Tzid)
	if err != nil {
		return nil, &cliErrors.Error{Code: "invalid_argument", Message: fmt.Sprintf("the event's %s time zone %q is not a known IANA name; pass --timezone to choose one", which, d.Tzid)}
	}
	return loc, nil
}

// chronosEventState is the part of a chronos event that update and delete need: the
// optimistic-lock timestamp, the current start/end zones, and whether it is a series.
type chronosEventState struct {
	Timestamp    int64
	ID           string
	SeriesID     string
	RRule        string
	RecurrenceID string
	Start        *chronosDateTime
	End          *chronosDateTime
}

// isSeriesMaster reports whether the event is the master of a recurring series, so that
// deleting it without a recurrence id removes every occurrence.
func (s chronosEventState) isSeriesMaster() bool {
	if s.RecurrenceID != "" {
		return false
	}
	return strings.TrimSpace(s.RRule) != "" || (s.SeriesID != "" && s.SeriesID == s.ID)
}

// flexibleString decodes a JSON string or number as a string.
func flexibleString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String()
	}
	return ""
}

// fetchEventState gets an event (or, with recurrenceID, one occurrence) and its current
// timestamp. The timestamp is the optimistic-locking "timestamp" parameter of the following
// update/delete, so a stale local copy cannot silently overwrite a newer change.
//
// The returned event must be the requested one. With recurrenceID, an occurrence that
// already has a change exception can come back as that exception: its own id, its
// seriesId equal to the requested series id, and the same recurrenceId. That is accepted,
// and State.ID then holds the exception's own id. The caller still addresses the update
// or delete by the series id plus recurrenceId: the chronos API documents that pair as
// the way to target one occurrence, and the server resolves it to an existing exception.
func fetchEventState(ctx context.Context, c *client.Client, sess client.Session, id, folderRef, recurrenceID string) (chronosEventState, error) {
	params := url.Values{"id": {id}, "folder": {folderRef}}
	if recurrenceID != "" {
		params.Set("recurrenceId", recurrenceID)
	}
	data, timestamp, err := c.DoWithTimestamp(ctx, http.MethodGet, "/chronos", "get", sess, params)
	if err != nil {
		return chronosEventState{}, err
	}
	var current struct {
		ID           string          `json:"id"`
		SeriesID     json.RawMessage `json:"seriesId"`
		RRule        string          `json:"rrule"`
		RecurrenceID json.RawMessage `json:"recurrenceId"`
		StartDate    json.RawMessage `json:"startDate"`
		EndDate      json.RawMessage `json:"endDate"`
	}
	if err := json.Unmarshal(data, &current); err != nil || strings.TrimSpace(current.ID) == "" {
		return chronosEventState{}, &cliErrors.Error{Code: "invalid_response", Message: "event timestamp response did not match the requested event"}
	}
	if current.ID != id {
		isException := recurrenceID != "" && flexibleString(current.SeriesID) == id &&
			recurrenceIDsMatch(flexibleString(current.RecurrenceID), recurrenceID)
		if !isException {
			return chronosEventState{}, &cliErrors.Error{Code: "invalid_response", Message: "event timestamp response did not match the requested event"}
		}
	}
	return chronosEventState{
		Timestamp:    timestamp,
		ID:           current.ID,
		SeriesID:     flexibleString(current.SeriesID),
		RRule:        current.RRule,
		RecurrenceID: flexibleString(current.RecurrenceID),
		Start:        decodeChronosDateTime(current.StartDate),
		End:          decodeChronosDateTime(current.EndDate),
	}, nil
}

// recurrenceIDsMatch reports whether two chronos recurrence ids name the same occurrence.
// The server can write a recurrence id in another form than the caller, for example
// "Europe/Berlin:20260708T110000" for "20260708T090000Z", so values that name a point in
// time are compared as instants. Floating and date-only values must be equal as text.
func recurrenceIDsMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	instantA, okA := recurrenceIDInstant(a)
	instantB, okB := recurrenceIDInstant(b)
	return okA && okB && instantA.Equal(instantB)
}

// recurrenceIDInstant parses a UTC ("...Z") or zone-prefixed ("Zone:local") recurrence id.
func recurrenceIDInstant(value string) (time.Time, bool) {
	if zone, local, found := strings.Cut(value, ":"); found {
		loc, err := time.LoadLocation(zone)
		if err != nil || zone == "" {
			return time.Time{}, false
		}
		t, err := time.ParseInLocation(chronosLocalFormat, local, loc)
		return t, err == nil
	}
	t, err := time.Parse(chronosTimeFormat, value)
	return t, err == nil
}

// eventUpdateTimes builds the startDate/endDate fields of an update. zone is the
// --timezone value or nil. current is the event as read before the update, or nil for a
// --dry-run preview, which has not read the event and shows --timezone or UTC.
//
// Without --timezone each boundary keeps the zone it has now, so an update does not move a
// recurring event to UTC. An all-day event keeps date-only values for both boundaries.
func eventUpdateTimes(start, end *time.Time, zone *time.Location, current *chronosEventState) (map[string]any, *cliErrors.Error) {
	fields := map[string]any{}
	if current != nil && current.Start.isDate() {
		startValue, endValue := current.Start.Value, ""
		if current.End != nil {
			endValue = current.End.Value
		}
		if start != nil {
			value := chronosDateIn(*start, zone)
			fields["startDate"] = value
			startValue = value["value"]
		}
		if end != nil {
			value := chronosDateIn(*end, zone)
			fields["endDate"] = value
			endValue = value["value"]
		}
		if endValue != "" && endValue <= startValue {
			return nil, &cliErrors.Error{Code: "invalid_argument", Message: fmt.Sprintf("the event is all-day: its end date %s must be after its start date %s; give both --start and --end", endValue, startValue)}
		}
		return fields, nil
	}
	if start != nil {
		loc := zone
		if loc == nil && current != nil {
			var cerr *cliErrors.Error
			if loc, cerr = current.Start.location("start"); cerr != nil {
				return nil, cerr
			}
		}
		fields["startDate"] = chronosDateTimeIn(*start, loc)
	}
	if end != nil {
		loc := zone
		if loc == nil && current != nil {
			var cerr *cliErrors.Error
			if loc, cerr = current.End.location("end"); cerr != nil {
				return nil, cerr
			}
		}
		fields["endDate"] = chronosDateTimeIn(*end, loc)
	}
	return fields, nil
}

// nowFunc is the clock for default time ranges; tests replace it.
var nowFunc = time.Now

// defaultRangeLength is the length of a time range without --to.
const defaultRangeLength = 30 * 24 * time.Hour

// defaultTimeRange parses --from and --to of a listing. Without --from the range starts
// now; without --to it ends defaultRangeLength after the start.
func defaultTimeRange(from, to string) (time.Time, time.Time, *cliErrors.Error) {
	start := nowFunc().UTC().Truncate(time.Second)
	if from != "" {
		parsed, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return time.Time{}, time.Time{}, invalidArgument("--from is not a valid RFC3339 timestamp: " + from)
		}
		start = parsed
	}
	end := start.Add(defaultRangeLength)
	if to != "" {
		parsed, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return time.Time{}, time.Time{}, invalidArgument("--to is not a valid RFC3339 timestamp: " + to)
		}
		end = parsed
	}
	return start, end, nil
}
