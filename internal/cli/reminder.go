// ABOUTME: The "reminder" commands: list reminders in a bounded time range via
// ABOUTME: GET /reminder?action=range, delete a reminder via PUT /reminder?action=delete, and
// ABOUTME: create a reminder via PUT /tasks?action=new with an "alarm" field: this deployment
// ABOUTME: has no standalone module=reminder create action (confirmed live: action=new/insert/
// ABOUTME: create on /reminder all return SVL-0021 "Unknown AJAX action"), but setting "alarm"
// ABOUTME: on a task creates a reminder row visible via reminder?action=range (confirmed live);
// ABOUTME: create then looks up that row so it can report the reminder id beside the task id.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const defaultReminderLimit = 50

// reminderModuleTasks is the ReminderData module number for tasks.
const reminderModuleTasks = 4

func init() {
	registerResource("reminder", runReminder)
	operations.Register(operations.Operation{
		Name:       "reminder.list",
		Resource:   "reminder",
		Action:     "list",
		Summary:    "List reminders due in a bounded time range",
		Keywords:   []string{"alarm", "remind"},
		Method:     http.MethodGet,
		Endpoint:   "/reminder",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"from":  {Type: "string", Required: false, Description: "range start, RFC3339 timestamp, e.g. 2026-07-01T00:00:00Z (default: now); applied by the CLI (alarm >= from) because the API has only an end parameter"},
			"to":    {Type: "string", Required: false, Description: "range end, RFC3339 timestamp, e.g. 2026-08-01T00:00:00Z (default: 30 days after the start); sent to the API as end"},
			"limit": {Type: "integer", Required: false, Description: "maximum reminders to return after the --from filter; default 50"},
		},
		Output: operations.OutputSpec{
			Type:         "list",
			Fields:       []string{"id", "module", "target_id", "folder", "alarm"},
			ScalarFields: []string{"id", "module", "target_id", "folder", "alarm"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "reminder.create",
		Resource:   "reminder",
		Action:     "create",
		Summary:    "Create a reminder by setting an alarm on a new task, then return the reminder id (for reminder.delete) and the task id (this deployment has no standalone module=reminder create action)",
		Keywords:   []string{"alarm", "remind"},
		Method:     http.MethodPut,
		Endpoint:   "/tasks",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"title":   {Type: "string", Required: true, Description: "title of the task the reminder is attached to"},
			"folder":  {Type: "string", Required: true, Description: "task folder id to create the reminder task in"},
			"at":      {Type: "string", Required: true, Description: "reminder time, RFC3339 timestamp, e.g. 2026-07-24T09:00:00Z"},
			"dry-run": {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "task_id", "folder_id", "alarm", "timestamp"},
			ScalarFields: []string{"id", "task_id", "folder_id", "alarm", "timestamp"},
			DryRunFields: []string{"dry_run", "would_create"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "reminder.delete",
		Resource:    "reminder",
		Action:      "delete",
		Summary:     "Delete a reminder",
		Method:      http.MethodPut,
		Endpoint:    "/reminder",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "integer", Required: true, Description: "positive reminder id, from reminder.list or the id output of reminder.create (not the task_id)"},
			"confirm": {Type: "string", Required: true, Description: "must exactly repeat the --id value to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate only, do not send the delete request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "id"},
			ScalarFields: []string{"deleted", "id"},
			DryRunFields: []string{"id", "dry_run"},
		},
	})
}

func runReminder(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("reminder requires an action, e.g. \"reminder list\""))
	}

	switch args[0] {
	case "list":
		return runReminderList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runReminderCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runReminderDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown reminder action: "+args[0]))
	}
}

type reminderItem struct {
	ID       string `json:"id"`
	Module   int    `json:"module"`
	TargetID string `json:"target_id"`
	Folder   string `json:"folder"`
	Alarm    int64  `json:"alarm"`
}

func runReminderList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("reminder.list", args,
		intFlag("limit", 1, math.MaxInt, "--limit must be a positive integer"))
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	from := f.String("from")
	to := f.String("to")
	limit := defaultReminderLimit
	if n, given := f.Int("limit"); given {
		limit = n
	}

	fromRange, toRange, rangeErr := defaultTimeRange(from, to)
	if rangeErr != nil {
		return exitWithError(stderr, llm, rangeErr)
	}
	fromTime, toTime := fromRange.UnixMilli(), toRange.UnixMilli()
	if toTime <= fromTime {
		return exitWithError(stderr, llm, invalidArgument("--to must be after --from"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	all, cerr := fetchReminderRange(ctx, c, sess, toTime)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	// reminder?action=range has only an end parameter, so the server returns every
	// reminder due before --to. --from is applied here, before --limit, so the limit and
	// the truncated flag describe the requested range.
	items := make([]reminderItem, 0, len(all))
	for _, it := range all {
		if it.Alarm >= fromTime {
			items = append(items, it)
		}
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Items     []reminderItem `json:"items"`
			Truncated bool           `json:"truncated"`
		}{Items: items, Truncated: truncated})
		return 0
	}

	header := []string{"id", "module", "target_id", "folder", "alarm"}
	rows := make([][]string, 0, len(items))
	for _, it := range items {
		rows = append(rows, []string{it.ID, strconv.Itoa(it.Module), it.TargetID, it.Folder, strconv.FormatInt(it.Alarm, 10)})
	}
	render.Table(stdout, header, rows)
	writeTruncationNotice(stdout, truncated, limit)
	return 0
}

// fetchReminderRange gets every reminder due before end (epoch milliseconds) through
// reminder?action=range and validates each row.
func fetchReminderRange(ctx context.Context, c *client.Client, sess client.Session, end int64) ([]reminderItem, *cliErrors.Error) {
	params := url.Values{"end": {strconv.FormatInt(end, 10)}}
	data, err := c.Do(ctx, http.MethodGet, "/reminder", "range", sess, params)
	if err != nil {
		return nil, toCLIError(err, "api_error")
	}

	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err != nil || rawItems == nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode reminder response"}
	}

	invalid := &cliErrors.Error{Code: "invalid_response", Message: "could not decode reminder response"}
	items := make([]reminderItem, 0, len(rawItems))
	for _, raw := range rawItems {
		var it struct {
			ID       json.Number `json:"id"`
			Module   *int        `json:"module"`
			TargetID json.Number `json:"target_id"`
			Folder   json.Number `json:"folder"`
			Alarm    *int64      `json:"alarm"`
		}
		if err := json.Unmarshal(raw, &it); err != nil || it.ID.String() == "" ||
			it.Module == nil || it.TargetID.String() == "" || it.Folder.String() == "" || it.Alarm == nil {
			return nil, invalid
		}
		if parsedID, err := it.ID.Int64(); err != nil || parsedID <= 0 {
			return nil, invalid
		}
		if *it.Module <= 0 {
			return nil, invalid
		}
		if targetID, err := it.TargetID.Int64(); err != nil || targetID <= 0 {
			return nil, invalid
		}
		if folderID, err := it.Folder.Int64(); err != nil || folderID <= 0 {
			return nil, invalid
		}
		if *it.Alarm <= 0 {
			return nil, invalid
		}
		items = append(items, reminderItem{
			ID:       it.ID.String(),
			Module:   *it.Module,
			TargetID: it.TargetID.String(),
			Folder:   it.Folder.String(),
			Alarm:    *it.Alarm,
		})
	}
	return items, nil
}

func runReminderCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("reminder.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	title := f.String("title")
	folder := f.String("folder")
	at := f.String("at")
	dryRun := f.Bool("dry-run")

	if title == "" {
		return exitWithError(stderr, llm, invalidArgument("--title is required"))
	}
	if folder == "" {
		return exitWithError(stderr, llm, invalidArgument("--folder is required"))
	}
	if at == "" {
		return exitWithError(stderr, llm, invalidArgument("--at is required, e.g. --at 2026-07-24T09:00:00Z"))
	}
	alarm, parseErr := parseRFC3339(at)
	if parseErr != nil {
		return exitWithError(stderr, llm, invalidArgument("--at is not a valid RFC3339 timestamp: "+at))
	}
	if alarm <= 0 {
		return exitWithError(stderr, llm, invalidArgument("--at must be after the Unix epoch"))
	}

	body := map[string]any{"folder_id": folder, "title": title, "alarm": alarm}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun      bool           `json:"dry_run"`
				WouldCreate map[string]any `json:"would_create"`
			}{DryRun: true, WouldCreate: body})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "folder_id", Value: folder},
			{Name: "title", Value: title},
			{Name: "dry_run", Value: "true"},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, timestamp, err := c.DoJSONWithTimestamp(ctx, http.MethodPut, "/tasks", "new", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var created struct {
		ID *json.Number `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode create-reminder response"})
	}
	id := created.ID.String()
	parsedID, idErr := strconv.ParseInt(id, 10, 64)
	if idErr != nil || parsedID <= 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode create-reminder response"})
	}

	// tasks?action=new returns only the task id, and reminder.delete needs the id of the
	// reminder row the alarm created. Find that row: the documented ReminderData carries
	// module 4 (tasks), target_id (the task id) and folder. When it cannot be found, the
	// task still exists, so task_id is reported on stdout for cleanup and the command fails.
	reminderID, lookupErr := findTaskReminder(ctx, c, sess, id, folder, alarm)
	result := reminderCreateResult{TaskID: id, FolderID: folder, Alarm: alarm, Timestamp: timestamp}
	if lookupErr == nil {
		result.ID = &reminderID
	}
	renderReminderCreateResult(stdout, llm, result)
	if lookupErr != nil {
		return exitWithError(stderr, llm, lookupErr)
	}
	return 0
}

// reminderCreateResult is the reminder create output. id is the reminder id that
// reminder.delete takes, or null when the reminder row could not be found; task_id is the
// backing task, which tasks.delete removes.
type reminderCreateResult struct {
	ID        *string `json:"id"`
	TaskID    string  `json:"task_id"`
	FolderID  string  `json:"folder_id"`
	Alarm     int64   `json:"alarm"`
	Timestamp int64   `json:"timestamp"`
}

func renderReminderCreateResult(stdout io.Writer, llm bool, result reminderCreateResult) {
	if llm {
		render.CompactJSON(stdout, result)
		return
	}
	id := ""
	if result.ID != nil {
		id = *result.ID
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: id},
		{Name: "task_id", Value: result.TaskID},
		{Name: "folder_id", Value: result.FolderID},
		{Name: "alarm", Value: strconv.FormatInt(result.Alarm, 10)},
	})
}

// reminderLookupWindow extends the range end past the alarm so a reminder due exactly at
// the alarm time is inside the range whether the server treats end as inclusive or not.
const reminderLookupWindow = int64(time.Minute / time.Millisecond)

// findTaskReminder returns the id of the reminder that a task alarm created. It reads
// reminder?action=range up to the alarm time and matches module 4 (tasks), target_id and
// folder; when more than one row matches, the alarm time decides.
func findTaskReminder(ctx context.Context, c *client.Client, sess client.Session, taskID, folder string, alarm int64) (string, *cliErrors.Error) {
	cleanup := fmt.Sprintf("; the task exists: remove it with tasks delete --id %s --folder %s if it is not wanted", taskID, folder)
	items, cerr := fetchReminderRange(ctx, c, sess, alarm+reminderLookupWindow)
	if cerr != nil {
		return "", &cliErrors.Error{
			Code:       cerr.Code,
			Message:    fmt.Sprintf("task %s was created, but its reminder could not be read: %s%s", taskID, cerr.Message, cleanup),
			HTTPStatus: cerr.HTTPStatus,
			Retryable:  cerr.Retryable,
			RequestID:  cerr.RequestID,
		}
	}
	var matches []reminderItem
	for _, it := range items {
		if it.Module == reminderModuleTasks && it.TargetID == taskID && it.Folder == folder {
			matches = append(matches, it)
		}
	}
	if len(matches) > 1 {
		var exact []reminderItem
		for _, it := range matches {
			if it.Alarm == alarm {
				exact = append(exact, it)
			}
		}
		matches = exact
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", &cliErrors.Error{Code: "reminder_not_found", Message: fmt.Sprintf("task %s was created with an alarm, but no reminder for it was found%s", taskID, cleanup)}
	default:
		return "", &cliErrors.Error{Code: "reminder_not_found", Message: fmt.Sprintf("task %s was created with an alarm, but %d reminders match it%s", taskID, len(matches), cleanup)}
	}
}

func runReminderDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("reminder.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	confirm, confirmSeen := f.Lookup("confirm")
	dryRun := f.Bool("dry-run")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	reminderID, parseErr := strconv.ParseInt(id, 10, 64)
	if parseErr != nil || reminderID <= 0 {
		return exitWithError(stderr, llm, invalidArgument("--id must be a positive integer"))
	}
	if !confirmSeen || confirm != id {
		return exitWithError(stderr, llm, invalidArgument("reminder delete requires --confirm "+id+" (must exactly repeat --id) to proceed"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				ID     string `json:"id"`
				DryRun bool   `json:"dry_run"`
			}{ID: id, DryRun: true})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "id", Value: id},
			{Name: "dry_run", Value: "true"},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	body := []map[string]int64{{"id": reminderID}}
	data, err := c.DoJSON(ctx, http.MethodPut, "/reminder", "delete", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if refused := deleteRefusalError(data, "reminder "+id); refused != nil {
		return exitWithError(stderr, llm, refused)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Deleted bool   `json:"deleted"`
			ID      string `json:"id"`
		}{Deleted: true, ID: id})
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "deleted", Value: "true"},
		{Name: "id", Value: id},
	})
	return 0
}

// parseRFC3339 parses an RFC3339 timestamp and returns it as Unix epoch milliseconds,
// the format the reminder module's range action expects for start/end.
func parseRFC3339(s string) (int64, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0, err
	}
	return t.UnixMilli(), nil
}
