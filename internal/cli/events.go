// ABOUTME: The "events" commands (list/get/create/update/delete/export/import) against the
// ABOUTME: deployment's calendar module, chronos (legacy "calendar" is unused).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

// chronosTimeFormat is the basic ISO 8601 UTC format the mailbox.org chronos module
// requires for rangeStart/rangeEnd, e.g. "20260701T000000Z".
const chronosTimeFormat = "20060102T150405Z"

const defaultEventsLimit = 50

// defaultCalendarFolder is the account's standard calendar folder id (see discovery
// notes: chronos folders must be addressed as "cal://0/<id>", not the bare id).
const defaultCalendarFolder = "32"

func chronosFolderRef(folder string) string {
	return "cal://0/" + folder
}

func init() {
	registerResource("events", runEvents)
	operations.Register(operations.Operation{
		Name:       "events.list",
		Resource:   "events",
		Action:     "list",
		Summary:    "List calendar events in a bounded time range, with each occurrence of a recurring series as its own item",
		Keywords:   []string{"calendar", "appointments", "meetings", "schedule", "agenda"},
		Method:     http.MethodGet,
		Endpoint:   "/chronos",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"from":      {Type: "string", Required: false, Description: "range start, RFC3339 timestamp, e.g. 2026-07-01T00:00:00Z (default: now)"},
			"to":        {Type: "string", Required: false, Description: "range end, RFC3339 timestamp, e.g. 2026-08-01T00:00:00Z (default: 30 days after the start)"},
			"limit":     {Type: "integer", Required: false, Description: "maximum events to return; default 50"},
			"no-expand": {Type: "bool", Required: false, Description: "return each recurring series once (its master) instead of one item per occurrence"},
		},
		Output: operations.OutputSpec{
			Type:         "list",
			Fields:       []string{"id", "summary", "startDate", "endDate", "recurrenceId", "seriesId"},
			ScalarFields: []string{"id", "summary"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "events.get",
		Resource:   "events",
		Action:     "get",
		Summary:    "Get one calendar event by id",
		Method:     http.MethodGet,
		Endpoint:   "/chronos",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":     {Type: "string", Required: true, Description: "event id"},
			"folder": {Type: "string", Required: false, Description: "calendar folder id; defaults to the account's standard calendar folder"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "summary", "startDate", "endDate", "description", "location"},
			ScalarFields: []string{"id", "summary", "description", "location"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "events.create",
		Resource:   "events",
		Action:     "create",
		Summary:    "Create a calendar event",
		Keywords:   []string{"calendar", "appointment", "meeting", "schedule", "book"},
		Method:     http.MethodPut,
		Endpoint:   "/chronos",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"summary":     {Type: "string", Required: true, Description: "event summary/title"},
			"start":       {Type: "string", Required: true, Description: "start time, RFC3339 timestamp"},
			"end":         {Type: "string", Required: true, Description: "end time, RFC3339 timestamp"},
			"folder":      {Type: "string", Required: false, Description: "calendar folder id; defaults to the account's standard calendar folder"},
			"description": {Type: "string", Required: false, Description: "event description"},
			"location":    {Type: "string", Required: false, Description: "event location"},
			"timezone":    {Type: "string", Required: false, Description: "IANA time zone for start and end, e.g. Europe/Berlin; default: the account's configured time zone, else UTC"},
			"dry-run":     {Type: "boolean", Required: false, Description: "validate and show what would be created without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "folder", "timezone", "event"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "events.update",
		Resource:   "events",
		Action:     "update",
		Summary:    "Update a calendar event's fields (optimistic locking via a fetched timestamp)",
		Method:     http.MethodPut,
		Endpoint:   "/chronos",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":            {Type: "string", Required: true, Description: "event id"},
			"folder":        {Type: "string", Required: false, Description: "calendar folder id; defaults to the account's standard calendar folder"},
			"summary":       {Type: "string", Required: false, Description: "new event summary/title"},
			"start":         {Type: "string", Required: false, Description: "new start time, RFC3339 timestamp"},
			"end":           {Type: "string", Required: false, Description: "new end time, RFC3339 timestamp"},
			"description":   {Type: "string", Required: false, Description: "new event description"},
			"location":      {Type: "string", Required: false, Description: "new event location"},
			"timezone":      {Type: "string", Required: false, Description: "IANA time zone for --start/--end, e.g. Europe/Berlin; default: keep the event's current zone. All-day events keep date-only values"},
			"recurrence-id": {Type: "string", Required: false, Description: "update only this occurrence of a recurring series, e.g. 20260708T090000Z (the recurrenceId from events.list)"},
			"dry-run":       {Type: "boolean", Required: false, Description: "validate and show what would be updated without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "recurrence_id"},
			ScalarFields: []string{"id", "recurrence_id"},
			DryRunFields: []string{"dry_run", "id", "folder", "recurrence_id", "fields"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "events.delete",
		Resource:    "events",
		Action:      "delete",
		Summary:     "Delete a calendar event or one occurrence (destructive; requires --confirm <id>; a whole recurring series also requires --series)",
		Method:      http.MethodPut,
		Endpoint:    "/chronos",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":            {Type: "string", Required: true, Description: "event id to delete"},
			"confirm":       {Type: "string", Required: true, Description: "must exactly equal --id to confirm deletion"},
			"folder":        {Type: "string", Required: false, Description: "calendar folder id; defaults to the account's standard calendar folder"},
			"recurrence-id": {Type: "string", Required: false, Description: "delete only this occurrence of a recurring series, e.g. 20260708T090000Z (the recurrenceId from events.list)"},
			"series":        {Type: "bool", Required: false, Description: "confirm that deleting a recurring series master removes every occurrence; required for that case"},
			"dry-run":       {Type: "boolean", Required: false, Description: "validate and show what would be deleted without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "id", "recurrence_id"},
			ScalarFields: []string{"deleted", "id", "recurrence_id"},
			DryRunFields: []string{"dry_run", "id", "folder", "recurrence_id", "series"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "events.export",
		Resource:   "events",
		Action:     "export",
		Summary:    "Export a calendar folder as iCal text",
		Method:     http.MethodGet,
		Endpoint:   "/export",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"folder": {Type: "string", Required: false, Description: "calendar folder id; defaults to the account's standard calendar folder"},
			"output": {Type: "string", Required: false, Description: "write iCal text to this file instead of stdout"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"saved_to", "bytes_written"},
			ScalarFields: []string{"saved_to", "bytes_written"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "events.import",
		Resource:   "events",
		Action:     "import",
		Summary:    "Import an iCal file as one or more calendar events",
		Method:     http.MethodPost,
		Endpoint:   "/import",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"file":    {Type: "string", Required: true, Description: "path to a local .ics file to import"},
			"folder":  {Type: "string", Required: false, Description: "calendar folder id; defaults to the account's standard calendar folder"},
			"dry-run": {Type: "boolean", Required: false, Description: "validate and show what would be sent without making a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "items", "count", "failed", "failed_count"},
			ScalarFields: []string{"id", "folder_id", "count", "failed_count"},
			DryRunFields: []string{"dry_run", "folder", "filename"},
		},
	})
}

func runEvents(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("events requires an action, e.g. \"events list\""))
	}

	switch args[0] {
	case "list":
		return runEventsList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runEventsGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runEventsCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runEventsUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runEventsDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "export":
		return runEventsExport(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "import":
		return runEventsImport(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown events action: "+args[0]))
	}
}

func runEventsList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("events.list", args,
		intFlag("limit", 1, math.MaxInt, "--limit must be a positive integer"))
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	from := f.String("from")
	to := f.String("to")
	limit := defaultEventsLimit
	if n, given := f.Int("limit"); given {
		limit = n
	}
	expand := !f.Bool("no-expand")

	fromTime, toTime, rangeErr := defaultTimeRange(from, to)
	if rangeErr != nil {
		return exitWithError(stderr, llm, rangeErr)
	}
	if !toTime.After(fromTime) {
		return exitWithError(stderr, llm, invalidArgument("--to must be after --from"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	// expand=true makes chronos return every occurrence of a recurring series in the range;
	// without it the occurrences are hidden behind the series master.
	params := url.Values{
		"rangeStart": {fromTime.UTC().Format(chronosTimeFormat)},
		"rangeEnd":   {toTime.UTC().Format(chronosTimeFormat)},
		"expand":     {strconv.FormatBool(expand)},
	}
	data, err := c.Do(ctx, http.MethodGet, "/chronos", "all", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var events []json.RawMessage
	if err := json.Unmarshal(data, &events); err != nil || events == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode events response"})
	}

	truncated := len(events) > limit
	if truncated {
		events = events[:limit]
	}
	decoded := make([]struct {
		ID           string          `json:"id"`
		RecurrenceID json.RawMessage `json:"recurrenceId"`
		Summary      string          `json:"summary"`
		StartDate    any             `json:"startDate"`
		EndDate      any             `json:"endDate"`
	}, len(events))
	for i, raw := range events {
		if err := json.Unmarshal(raw, &decoded[i]); err != nil || strings.TrimSpace(decoded[i].ID) == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("event %d in events response is malformed", i)})
		}
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Items     []json.RawMessage `json:"items"`
			Count     int               `json:"count"`
			Truncated bool              `json:"truncated"`
		}{Items: events, Count: len(events), Truncated: truncated})
		return 0
	}

	// Occurrences of an expanded series share the series id, so the recurrence_id column
	// (always shown, empty for a single event) tells them apart and is the value
	// --recurrence-id takes.
	rows := make([][]string, len(decoded))
	for i, ev := range decoded {
		rows[i] = []string{ev.ID, flexibleString(ev.RecurrenceID), ev.Summary, formatEventTime(ev.StartDate), formatEventTime(ev.EndDate)}
	}
	render.Table(stdout, []string{"id", "recurrence_id", "summary", "start", "end"}, rows)
	writeTruncationNotice(stdout, truncated, limit)
	return 0
}

type eventDetail struct {
	ID          string `json:"id"`
	Summary     string `json:"summary"`
	StartDate   any    `json:"startDate"`
	EndDate     any    `json:"endDate"`
	Description string `json:"description"`
	Location    string `json:"location"`
}

func runEventsGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("events.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	folder := defaultCalendarFolder
	if value, given := f.Lookup("folder"); given {
		folder = value
	}

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	params := url.Values{"id": {id}, "folder": {chronosFolderRef(folder)}}
	data, err := c.Do(ctx, http.MethodGet, "/chronos", "get", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var ev eventDetail
	if err := json.Unmarshal(data, &ev); err != nil || strings.TrimSpace(ev.ID) == "" || ev.ID != id {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode event response"})
	}

	if llm {
		render.CompactJSON(stdout, ev)
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: ev.ID},
		{Name: "summary", Value: ev.Summary},
		{Name: "start", Value: formatEventTime(ev.StartDate)},
		{Name: "end", Value: formatEventTime(ev.EndDate)},
		{Name: "description", Value: ev.Description},
		{Name: "location", Value: ev.Location},
	})
	return 0
}

func runEventsCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("events.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	summary := f.String("summary")
	start := f.String("start")
	end := f.String("end")
	folder := defaultCalendarFolder
	if value, given := f.Lookup("folder"); given {
		folder = value
	}
	description := f.String("description")
	location := f.String("location")
	timezone, timezoneSet := f.Lookup("timezone")

	if summary == "" {
		return exitWithError(stderr, llm, invalidArgument("--summary is required"))
	}
	if start == "" {
		return exitWithError(stderr, llm, invalidArgument("--start is required, e.g. --start 2026-07-01T09:00:00Z"))
	}
	if end == "" {
		return exitWithError(stderr, llm, invalidArgument("--end is required, e.g. --end 2026-07-01T09:30:00Z"))
	}

	startTime, parseErr := time.Parse(time.RFC3339, start)
	if parseErr != nil {
		return exitWithError(stderr, llm, invalidArgument("--start is not a valid RFC3339 timestamp: "+start))
	}
	endTime, parseErr := time.Parse(time.RFC3339, end)
	if parseErr != nil {
		return exitWithError(stderr, llm, invalidArgument("--end is not a valid RFC3339 timestamp: "+end))
	}
	if !endTime.After(startTime) {
		return exitWithError(stderr, llm, invalidArgument("--end must be after --start"))
	}

	var zone *time.Location
	if timezoneSet {
		var cerr *cliErrors.Error
		if zone, cerr = loadEventZone(timezone); cerr != nil {
			return exitWithError(stderr, llm, cerr)
		}
	}

	body := map[string]any{"summary": summary}
	if description != "" {
		body["description"] = description
	}
	if location != "" {
		body["location"] = location
	}
	setEventTimes := func(loc *time.Location) {
		body["startDate"] = chronosDateTimeIn(startTime, loc)
		body["endDate"] = chronosDateTimeIn(endTime, loc)
	}

	if dryRun {
		// The account time zone is read only at run time, so without --timezone the
		// preview shows UTC values and says which zone the real run uses.
		setEventTimes(zone)
		zoneLabel := timezone
		if !timezoneSet {
			zoneLabel = "account time zone at run time (preview shows UTC)"
		}
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun   bool           `json:"dry_run"`
				Folder   string         `json:"folder"`
				Timezone string         `json:"timezone"`
				Event    map[string]any `json:"event"`
			}{DryRun: true, Folder: folder, Timezone: zoneLabel, Event: body})
			return 0
		}
		fmt.Fprintf(stdout, "Would create event %q in folder %s, time zone %s (dry run, no request sent)\n", summary, folder, zoneLabel)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	// Without --timezone, use the account's configured zone so a recurring event keeps its
	// local time across daylight saving changes; UTC is the last fallback.
	if zone == nil {
		if configured, ok := configuredTimeZone(ctx, c, sess); ok {
			zone = configured
		}
	}
	setEventTimes(zone)

	params := url.Values{"folder": {chronosFolderRef(folder)}}
	data, err := c.DoJSON(ctx, http.MethodPut, "/chronos", "new", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	// PUT /chronos?action=new responds with a CalendarResult
	// ({"created":[...],"updated":[],"deleted":[]}), not a bare event object, since a
	// single create request can in principle affect multiple occurrences.
	var result struct {
		Created []struct {
			ID string `json:"id"`
		} `json:"created"`
	}
	if err := json.Unmarshal(data, &result); err != nil || len(result.Created) == 0 || strings.TrimSpace(result.Created[0].ID) == "" {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode create response"})
	}
	id := result.Created[0].ID

	if llm {
		render.CompactJSON(stdout, struct {
			ID string `json:"id"`
		}{ID: id})
		return 0
	}
	fmt.Fprintf(stdout, "Created event %s\n", id)
	return 0
}

func runEventsUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("events.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	folder := defaultCalendarFolder
	if value, given := f.Lookup("folder"); given {
		folder = value
	}
	timezone, timezoneSet := f.Lookup("timezone")
	recurrenceID := f.String("recurrence-id")
	// A field flag given with an empty value still changes the field, so keep "given".
	optional := func(name string) *string {
		if value, given := f.Lookup(name); given {
			return &value
		}
		return nil
	}
	summary, start, end := optional("summary"), optional("start"), optional("end")
	description, location := optional("description"), optional("location")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if summary == nil && start == nil && end == nil && description == nil && location == nil {
		return exitWithError(stderr, llm, invalidArgument("at least one of --summary, --start, --end, --description, --location is required"))
	}
	if cerr := validateRecurrenceID(recurrenceID); cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	var zone *time.Location
	if timezoneSet {
		if start == nil && end == nil {
			return exitWithError(stderr, llm, invalidArgument("--timezone applies to --start and --end; give at least one of them"))
		}
		var cerr *cliErrors.Error
		if zone, cerr = loadEventZone(timezone); cerr != nil {
			return exitWithError(stderr, llm, cerr)
		}
	}

	body := map[string]any{}
	var startTime, endTime *time.Time
	if summary != nil {
		body["summary"] = *summary
	}
	if description != nil {
		body["description"] = *description
	}
	if location != nil {
		body["location"] = *location
	}
	if start != nil {
		parsed, parseErr := time.Parse(time.RFC3339, *start)
		if parseErr != nil {
			return exitWithError(stderr, llm, invalidArgument("--start is not a valid RFC3339 timestamp: "+*start))
		}
		startTime = &parsed
	}
	if end != nil {
		parsed, parseErr := time.Parse(time.RFC3339, *end)
		if parseErr != nil {
			return exitWithError(stderr, llm, invalidArgument("--end is not a valid RFC3339 timestamp: "+*end))
		}
		endTime = &parsed
	}
	if startTime != nil && endTime != nil && !endTime.After(*startTime) {
		return exitWithError(stderr, llm, invalidArgument("--end must be after --start"))
	}

	if dryRun {
		// The preview has not read the event, so it shows --start/--end in --timezone or
		// UTC; the real run keeps the event's current zone and all-day format.
		timeFields, cerr := eventUpdateTimes(startTime, endTime, zone, nil)
		if cerr != nil {
			return exitWithError(stderr, llm, cerr)
		}
		for key, value := range timeFields {
			body[key] = value
		}
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun       bool           `json:"dry_run"`
				ID           string         `json:"id"`
				Folder       string         `json:"folder"`
				RecurrenceID string         `json:"recurrence_id,omitempty"`
				Fields       map[string]any `json:"fields"`
			}{DryRun: true, ID: id, Folder: folder, RecurrenceID: recurrenceID, Fields: body})
			return 0
		}
		target := "event " + id
		if recurrenceID != "" {
			target += " occurrence " + recurrenceID
		}
		fmt.Fprintf(stdout, "Would update %s in folder %s (dry run, no request sent)\n", target, folder)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	folderRef := chronosFolderRef(folder)
	current, err := fetchEventState(ctx, c, sess, id, folderRef, recurrenceID)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	timeFields, cerr := eventUpdateTimes(startTime, endTime, zone, &current)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	for key, value := range timeFields {
		body[key] = value
	}

	params := url.Values{"id": {id}, "folder": {folderRef}, "timestamp": {strconv.FormatInt(current.Timestamp, 10)}}
	if recurrenceID != "" {
		params.Set("recurrenceId", recurrenceID)
	}
	data, err := c.DoJSON(ctx, http.MethodPut, "/chronos", "update", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if resultErr := eventOccurrenceUpdateResultError(data, id, recurrenceID != ""); resultErr != nil {
		return exitWithError(stderr, llm, resultErr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID           string `json:"id"`
			RecurrenceID string `json:"recurrence_id,omitempty"`
		}{ID: id, RecurrenceID: recurrenceID})
		return 0
	}
	if recurrenceID != "" {
		fmt.Fprintf(stdout, "Updated event %s occurrence %s\n", id, recurrenceID)
		return 0
	}
	fmt.Fprintf(stdout, "Updated event %s\n", id)
	return 0
}

// validateRecurrenceID checks the shape of a --recurrence-id value. The chronos API accepts
// a UTC date-time (20170708T220000Z), a floating date-time, a date (20170708), or a value
// with a zone prefix (Europe/Berlin:20170708T220000).
func validateRecurrenceID(value string) *cliErrors.Error {
	if value == "" {
		return nil
	}
	invalid := &cliErrors.Error{Code: "invalid_argument", Message: "--recurrence-id must look like 20260708T090000Z, 20260708T090000, 20260708 or Europe/Berlin:20260708T090000, got: " + value}
	local := value
	if zone, rest, found := strings.Cut(value, ":"); found {
		if _, err := time.LoadLocation(zone); err != nil || zone == "" {
			return invalid
		}
		local = rest
	}
	for _, layout := range []string{chronosTimeFormat, chronosLocalFormat, chronosDateFormat} {
		if _, err := time.Parse(layout, local); err == nil {
			return nil
		}
	}
	return invalid
}

// eventUpdateResultError accepts the Chronos CalendarResult used by current OX
// deployments, as well as legacy empty responses and deployments that echo the exact
// event id. A CalendarResult must name the requested event in its updated collection;
// the live matrix separately proves the observable updated fields through events.get.
func eventUpdateResultError(data []byte, requestedID string) *cliErrors.Error {
	return eventOccurrenceUpdateResultError(data, requestedID, false)
}

// eventOccurrenceUpdateResultError is eventUpdateResultError with occurrence support. For
// an occurrence update the server may create a change exception instead, so an updated or
// created event whose seriesId is the requested id also counts.
func eventOccurrenceUpdateResultError(data []byte, requestedID string, occurrence bool) *cliErrors.Error {
	var result any
	shape := "invalid JSON"
	if err := json.Unmarshal(data, &result); err == nil {
		switch value := result.(type) {
		case nil:
			return nil
		case map[string]any:
			shape = fmt.Sprintf("object(fields=%d)", len(value))
			if len(value) == 0 {
				return nil
			}
			if id, ok := value["id"].(string); ok && id == requestedID {
				return nil
			}
			collections := []string{"updated"}
			if occurrence {
				collections = append(collections, "created")
			}
			for _, collection := range collections {
				items, _ := value[collection].([]any)
				for _, item := range items {
					event, ok := item.(map[string]any)
					if !ok {
						continue
					}
					if id, ok := event["id"].(string); ok && id == requestedID && collection == "updated" {
						return nil
					}
					if seriesID, ok := event["seriesId"].(string); ok && occurrence && seriesID == requestedID {
						return nil
					}
				}
			}
		case []any:
			matchingIDs := 0
			for _, item := range value {
				if object, ok := item.(map[string]any); ok {
					if id, ok := object["id"].(string); ok && id == requestedID {
						matchingIDs++
					}
				}
			}
			shape = fmt.Sprintf("array(length=%d,matching_ids=%d)", len(value), matchingIDs)
		default:
			shape = fmt.Sprintf("%T", result)
		}
	}
	return &cliErrors.Error{
		Code:    "invalid_response",
		Message: "events update response did not match requested event " + requestedID + " (got " + shape + ")",
	}
}

func runEventsDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("events.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	series := f.Bool("series")
	id := f.String("id")
	confirm := f.String("confirm")
	folder := defaultCalendarFolder
	if value, given := f.Lookup("folder"); given {
		folder = value
	}
	recurrenceID := f.String("recurrence-id")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("--confirm <id> is required to delete an event; it must exactly equal --id"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm value does not match --id; refusing to delete"))
	}
	if cerr := validateRecurrenceID(recurrenceID); cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}
	if series && recurrenceID != "" {
		return exitWithError(stderr, llm, invalidArgument("--series deletes a whole series and --recurrence-id deletes one occurrence; give only one of them"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun       bool   `json:"dry_run"`
				ID           string `json:"id"`
				Folder       string `json:"folder"`
				RecurrenceID string `json:"recurrence_id,omitempty"`
				Series       bool   `json:"series,omitempty"`
			}{DryRun: true, ID: id, Folder: folder, RecurrenceID: recurrenceID, Series: series})
			return 0
		}
		target := "event " + id
		if recurrenceID != "" {
			target += " occurrence " + recurrenceID
		}
		fmt.Fprintf(stdout, "Would delete %s in folder %s (dry run, no request sent)\n", target, folder)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	folderRef := chronosFolderRef(folder)
	current, err := fetchEventState(ctx, c, sess, id, folderRef, recurrenceID)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	// Deleting a series master removes every occurrence. events list returns occurrences
	// with the series id, so that must be an explicit choice, not a side effect.
	if recurrenceID == "" && current.isSeriesMaster() && !series {
		return exitWithError(stderr, llm, invalidArgument("event "+id+" is a recurring series; deleting it removes every occurrence. Add --series to delete the whole series, or --recurrence-id <recurrenceId> to delete one occurrence"))
	}

	params := url.Values{"timestamp": {strconv.FormatInt(current.Timestamp, 10)}}
	target := map[string]string{"id": id, "folder": folderRef}
	if recurrenceID != "" {
		target["recurrenceId"] = recurrenceID
	}
	body := []map[string]string{target}
	data, err := c.DoJSON(ctx, http.MethodPut, "/chronos", "delete", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	var exceptionIDs []string
	if recurrenceID != "" && current.ID != id {
		// The occurrence is a change exception with its own id; a result that names it
		// as deleted also confirms the delete.
		exceptionIDs = append(exceptionIDs, current.ID)
	}
	if resultErr := chronosOccurrenceDeleteResultError(data, id, folderRef, recurrenceID != "", exceptionIDs...); resultErr != nil {
		return exitWithError(stderr, llm, resultErr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Deleted      bool   `json:"deleted"`
			ID           string `json:"id"`
			RecurrenceID string `json:"recurrence_id,omitempty"`
		}{Deleted: true, ID: id, RecurrenceID: recurrenceID})
		return 0
	}
	if recurrenceID != "" {
		fmt.Fprintf(stdout, "Deleted event %s occurrence %s\n", id, recurrenceID)
		return 0
	}
	fmt.Fprintf(stdout, "Deleted event %s\n", id)
	return 0
}

// chronosDeleteResultError validates the per-event CalendarResults returned by
// chronos?action=delete. Unlike legacy delete endpoints, a non-empty data array is the
// normal success shape. Success still requires a result and deleted entry that both name
// the requested event and folder; a nested error object reports an application-level
// refusal.
func chronosDeleteResultError(data []byte, eventID, folderRef string) *cliErrors.Error {
	return chronosOccurrenceDeleteResultError(data, eventID, folderRef, false)
}

// chronosOccurrenceDeleteResultError is chronosDeleteResultError with occurrence support.
// Deleting one occurrence can leave the series in place and update its master with a
// delete exception, so for an occurrence an updated entry for the event also counts.
// exceptionIDs are the own ids of a change exception for that occurrence; a deleted entry
// that names one also confirms the delete.
func chronosOccurrenceDeleteResultError(data []byte, eventID, folderRef string, occurrence bool, exceptionIDs ...string) *cliErrors.Error {
	type eventRef struct {
		Folder string `json:"folder"`
		ID     string `json:"id"`
	}
	var results []struct {
		FolderID string              `json:"folderId"`
		ID       string              `json:"id"`
		Deleted  []eventRef          `json:"deleted"`
		Updated  []eventRef          `json:"updated"`
		Error    *cliErrors.Envelope `json:"error"`
	}
	if err := json.Unmarshal(data, &results); err != nil {
		return &cliErrors.Error{
			Code:    "invalid_response",
			Message: "could not decode chronos delete response",
		}
	}
	if len(results) == 0 {
		return &cliErrors.Error{
			Code:    "invalid_response",
			Message: "chronos delete response did not contain a result for event " + eventID,
		}
	}
	for _, result := range results {
		if result.ID != eventID || result.FolderID != folderRef {
			continue
		}
		if result.Error != nil {
			resultErr := cliErrors.FromEnvelope(*result.Error, http.StatusOK)
			if resultErr.Code == "" {
				resultErr.Code = "api_error"
			}
			if resultErr.Message == "" {
				resultErr.Message = result.Error.ErrorDesc
			}
			if resultErr.Message == "" {
				resultErr.Message = "event " + eventID + " was not deleted"
			}
			return resultErr
		}
		confirmations := result.Deleted
		if occurrence {
			confirmations = append(confirmations, result.Updated...)
		}
		for _, deleted := range confirmations {
			if deleted.ID == eventID && (deleted.Folder == "" || deleted.Folder == folderRef) {
				return nil
			}
		}
		if occurrence {
			for _, deleted := range result.Deleted {
				for _, exceptionID := range exceptionIDs {
					if deleted.ID == exceptionID && (deleted.Folder == "" || deleted.Folder == folderRef) {
						return nil
					}
				}
			}
		}
		return &cliErrors.Error{Code: "invalid_response", Message: "chronos delete response did not confirm deletion of event " + eventID}
	}
	return &cliErrors.Error{Code: "invalid_response", Message: "chronos delete response did not contain a result for event " + eventID}
}

// formatEventTime renders a chronos startDate/endDate value (typically an object like
// {"value":"20260701T090000Z"}, but sometimes absent) as a short display string. A local
// value with a tzid other than UTC is shown with its zone, for example
// "20260701T110000 Europe/Berlin", because the value alone does not say which zone it is in.
func formatEventTime(v any) string {
	if m, ok := v.(map[string]any); ok {
		if val, ok := m["value"].(string); ok {
			if tzid, _ := m["tzid"].(string); tzid != "" && !strings.HasSuffix(val, "Z") && !utcZoneNames[tzid] {
				return val + " " + tzid
			}
			return val
		}
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// utcZoneNames are tzid values that mean UTC, so formatEventTime does not show them.
var utcZoneNames = map[string]bool{
	"UTC": true, "Etc/UTC": true, "GMT": true, "Etc/GMT": true, "UCT": true, "Etc/UCT": true,
	"Universal": true, "Etc/Universal": true, "Zulu": true, "Etc/Zulu": true,
}

func runEventsExport(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("events.export", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	folder := defaultCalendarFolder
	if value, given := f.Lookup("folder"); given {
		folder = value
	}
	output := f.String("output")

	if cerr := requireOutputFileForDocument(llm, output, "iCal"); cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	content, err := c.DoRaw(ctx, http.MethodGet, "/export", "ICAL", sess, url.Values{"folder": {folder}})
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if output == "" {
		stdout.Write(content)
		return 0
	}

	if err := writePrivateFile(output, content); err != nil {
		return exitWithError(stderr, llm, invalidArgument("could not write --output file: "+err.Error()))
	}

	result := struct {
		SavedTo      string `json:"saved_to"`
		BytesWritten int    `json:"bytes_written"`
	}{SavedTo: output, BytesWritten: len(content)}
	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "saved_to", Value: result.SavedTo},
		{Name: "bytes_written", Value: strconv.Itoa(result.BytesWritten)},
	})
	return 0
}

func runEventsImport(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("events.import", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	path := f.String("file")
	folder := defaultCalendarFolder
	if value, given := f.Lookup("folder"); given {
		folder = value
	}

	if path == "" {
		return exitWithError(stderr, llm, invalidArgument("--file is required, e.g. --file calendar.ics"))
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.Mode().IsRegular() {
		return exitWithError(stderr, llm, invalidArgument("--file does not exist or is not a regular readable file: "+path))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun   bool   `json:"dry_run"`
				Folder   string `json:"folder"`
				Filename string `json:"filename"`
			}{DryRun: true, Folder: folder, Filename: filepath.Base(path)})
			return 0
		}
		fmt.Fprintf(stdout, "Would import %s into folder %s (dry run, no request sent)\n", filepath.Base(path), folder)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	file, openErr := os.Open(path)
	if openErr != nil {
		return exitWithError(stderr, llm, invalidArgument("could not open --file: "+openErr.Error()))
	}
	defer file.Close()

	params := url.Values{"folder": {folder}}
	data, err := c.DoMultipart(ctx, "/import", "ICAL", sess, params, struct{}{}, filepath.Base(path), "text/calendar", file)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var results []importResult
	if err := json.Unmarshal(data, &results); err != nil || len(results) == 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode events import response"})
	}
	return emitImportOutput(stdout, stderr, llm, results, folder, "events")
}
