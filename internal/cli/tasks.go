// ABOUTME: The "tasks" resource commands: list/get/create/update/delete tasks via the
// ABOUTME: OX /tasks module, with folder discovery and timestamp-based optimistic locking.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const defaultTasksLimit = 50

// tasksListColumns are the OX task field codes requested from tasks.all, in the order the
// response rows carry them: object id, title, end date, status. OX numbers columns per
// module: 200 and 202 come from the block shared by appointments and tasks, while a task's
// status is 300, in the task-specific block.
const tasksListColumns = "1,200,202,300"

func init() {
	registerResource("tasks", runTasks)
	operations.Register(operations.Operation{
		Name:       "tasks.list",
		Resource:   "tasks",
		Action:     "list",
		Summary:    "List tasks in a folder (defaults to the standard tasks folder)",
		Keywords:   []string{"todo", "to-do"},
		Method:     http.MethodGet,
		Endpoint:   "/tasks",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"folder": {Type: "string", Required: false, Description: "folder id; defaults to the account's standard tasks folder"},
			"limit":  {Type: "int", Required: false, Description: "maximum number of tasks to return (default 50)"},
		},
		Output: operations.OutputSpec{
			Type:         "list",
			Fields:       []string{"id", "title", "status", "end_date"},
			ScalarFields: []string{"id", "title", "status", "end_date"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "tasks.get",
		Resource:   "tasks",
		Action:     "get",
		Summary:    "Get a single task by id",
		Method:     http.MethodGet,
		Endpoint:   "/tasks",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":     {Type: "string", Required: true, Description: "task id"},
			"folder": {Type: "string", Required: false, Description: "folder id; defaults to the account's standard tasks folder"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "title", "note", "status", "priority", "end_date", "timestamp"},
			ScalarFields: []string{"id", "folder_id", "title", "note", "status", "priority", "end_date", "timestamp"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "tasks.create",
		Resource:   "tasks",
		Action:     "create",
		Summary:    "Create a task in a folder (defaults to the standard tasks folder)",
		Keywords:   []string{"todo", "to-do"},
		Method:     http.MethodPut,
		Endpoint:   "/tasks",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"title":    {Type: "string", Required: true, Description: "task title"},
			"note":     {Type: "string", Required: false, Description: "task description/note"},
			"status":   {Type: "int", Required: false, Description: "1=not started, 2=in progress, 3=done, 4=waiting, 5=deferred"},
			"priority": {Type: "int", Required: false, Description: "1=low, 2=medium, 3=high"},
			"end-date": {Type: "string", Required: false, Description: "due date, RFC3339 timestamp"},
			"folder":   {Type: "string", Required: false, Description: "folder id; defaults to the account's standard tasks folder"},
			"dry-run":  {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "timestamp"},
			ScalarFields: []string{"id", "folder_id", "timestamp"},
			DryRunFields: []string{"dry_run", "would_create"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "tasks.update",
		Resource:   "tasks",
		Action:     "update",
		Summary:    "Update fields on an existing task",
		Method:     http.MethodPut,
		Endpoint:   "/tasks",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":        {Type: "string", Required: true, Description: "task id"},
			"timestamp": {Type: "int", Required: true, Description: "last-known timestamp of the task, from tasks.get or tasks.create, for optimistic locking"},
			"folder":    {Type: "string", Required: false, Description: "folder id; defaults to the account's standard tasks folder"},
			"title":     {Type: "string", Required: false, Description: "new task title"},
			"note":      {Type: "string", Required: false, Description: "new task description/note"},
			"status":    {Type: "int", Required: false, Description: "1=not started, 2=in progress, 3=done, 4=waiting, 5=deferred"},
			"priority":  {Type: "int", Required: false, Description: "1=low, 2=medium, 3=high"},
			"end-date":  {Type: "string", Required: false, Description: "due date, RFC3339 timestamp"},
			"dry-run":   {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "updated", "timestamp"},
			ScalarFields: []string{"id", "updated", "timestamp"},
			DryRunFields: []string{"dry_run", "id", "would_update"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "tasks.delete",
		Resource:    "tasks",
		Action:      "delete",
		Summary:     "Delete a task (destructive; requires --confirm <id>)",
		Method:      http.MethodPut,
		Endpoint:    "/tasks",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":        {Type: "string", Required: true, Description: "task id"},
			"timestamp": {Type: "int", Required: true, Description: "last-known timestamp of the task, from tasks.get, for optimistic locking"},
			"confirm":   {Type: "string", Required: true, Description: "must exactly repeat the task id being deleted"},
			"folder":    {Type: "string", Required: false, Description: "folder id; defaults to the account's standard tasks folder"},
			"dry-run":   {Type: "bool", Required: false, Description: "validate only, do not send the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "deleted"},
			ScalarFields: []string{"id", "deleted"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
}

func runTasks(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("tasks requires an action; run \"mbx commands tasks\" to list available task commands"))
	}

	switch args[0] {
	case "list":
		return runTasksList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runTasksGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runTasksCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runTasksUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runTasksDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown tasks action: "+args[0]+"; run \"mbx commands tasks\" to list available task commands"))
	}
}

type taskItem struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  *int   `json:"status"`
	EndDate *int64 `json:"end_date"`
}

func runTasksList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("tasks.list", args,
		intFlag("limit", 1, math.MaxInt, "--limit must be a positive integer"))
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	folder := f.String("folder")
	limit := defaultTasksLimit
	if n, given := f.Int("limit"); given {
		limit = n
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		discovered, err := discoverTasksFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		if discovered == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "no standard tasks folder found; pass --folder explicitly"})
		}
		folder = discovered
	}

	params := url.Values{"folder": {folder}, "columns": {tasksListColumns}}
	data, err := c.Do(ctx, http.MethodGet, "/tasks", "all", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode tasks response"})
	}

	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}

	items := make([]taskItem, 0, len(rows))
	for i, row := range rows {
		item, rowErr := decodeTaskListRow(row)
		if rowErr != nil {
			cerr := &cliErrors.Error{
				Code:    "invalid_response",
				Message: fmt.Sprintf("could not decode tasks row %d: %v", i+1, rowErr),
			}
			return exitWithError(stderr, llm, cerr)
		}
		items = append(items, item)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Items     []taskItem `json:"items"`
			Truncated bool       `json:"truncated"`
		}{Items: items, Truncated: truncated})
		return 0
	}

	header := []string{"id", "title", "status", "end_date"}
	tableRows := make([][]string, 0, len(items))
	for _, it := range items {
		endDate := ""
		if it.EndDate != nil {
			endDate = strconv.FormatInt(*it.EndDate, 10)
		}
		status := ""
		if it.Status != nil {
			status = strconv.Itoa(*it.Status)
		}
		tableRows = append(tableRows, []string{it.ID, it.Title, status, endDate})
	}
	render.Table(stdout, header, tableRows)
	writeTruncationNotice(stdout, truncated, limit)
	return 0
}

func decodeTaskListRow(row []json.RawMessage) (taskItem, error) {
	if len(row) != 4 {
		return taskItem{}, fmt.Errorf("got %d columns, want 4", len(row))
	}
	var id json.Number
	var title *string
	var endDate *int64
	var status *int
	if err := json.Unmarshal(row[0], &id); err != nil || id.String() == "" {
		return taskItem{}, fmt.Errorf("id column is not a number")
	}
	if parsedID, err := id.Int64(); err != nil || parsedID <= 0 {
		return taskItem{}, fmt.Errorf("id column is not a positive integer")
	}
	if err := json.Unmarshal(row[1], &title); err != nil {
		return taskItem{}, fmt.Errorf("title column is not a string")
	}
	if title == nil {
		return taskItem{}, fmt.Errorf("title column is null")
	}
	if err := json.Unmarshal(row[2], &endDate); err != nil {
		return taskItem{}, fmt.Errorf("end_date column is neither a timestamp nor null")
	}
	if endDate != nil && *endDate <= 0 {
		return taskItem{}, fmt.Errorf("end_date column is not a positive timestamp")
	}
	if err := json.Unmarshal(row[3], &status); err != nil || (status != nil && (*status < 1 || *status > 5)) {
		return taskItem{}, fmt.Errorf("status column is neither null nor an integer from 1 to 5")
	}
	return taskItem{ID: id.String(), Title: *title, Status: status, EndDate: endDate}, nil
}

// discoverTasksFolder finds the account's standard tasks folder. The documented source is
// GET /config/folder/tasks; when that read fails or gives no usable id, it falls back to
// listing private folders (listTasksFolder), which can only pick the first tasks folder.
func discoverTasksFolder(ctx context.Context, c *client.Client, sess client.Session) (string, error) {
	if id, ok := configuredTasksFolder(ctx, c, sess); ok {
		return id, nil
	}
	return listTasksFolder(ctx, c, sess)
}

// configuredTasksFolder reads the standard tasks folder id from the user configuration.
// The documentation gives config leaf values as strings, but folder ids are often numbers,
// so both are accepted.
func configuredTasksFolder(ctx context.Context, c *client.Client, sess client.Session) (string, bool) {
	data, err := c.Do(ctx, http.MethodGet, "/config/folder/tasks", "", sess, nil)
	if err != nil {
		return "", false
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false
	}
	var id string
	switch v := value.(type) {
	case string:
		id = strings.TrimSpace(v)
	case json.Number:
		id = v.String()
	}
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n <= 0 {
		return "", false
	}
	return id, true
}

// listTasksFolder finds a tasks folder by listing the folders under the IPM root and
// picking the row whose module (column 301) is "tasks". The module=tasks query filter is
// not honored server-side on this deployment (it returns every folder regardless of
// module), so this filters client-side rather than trusting the filter or assuming the
// first row. Returns "" if none is found.
func listTasksFolder(ctx context.Context, c *client.Client, sess client.Session) (string, error) {
	params := url.Values{"parent": {"1"}, "tree": {"1"}, "module": {"tasks"}, "columns": {"1,301"}}
	data, err := c.Do(ctx, http.MethodGet, "/folders", "list", sess, params)
	if err != nil {
		return "", err
	}

	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil {
		return "", err
	}
	if rows == nil {
		return "", &cliErrors.Error{Code: "invalid_response", Message: "folder discovery response contained a null collection"}
	}

	for _, row := range rows {
		if len(row) != 2 {
			return "", &cliErrors.Error{Code: "invalid_response", Message: "folder discovery response contained an unexpected number of columns"}
		}
		var module string
		if err := json.Unmarshal(row[1], &module); err != nil {
			return "", &cliErrors.Error{Code: "invalid_response", Message: "could not decode folder discovery response"}
		}
		if module == "tasks" {
			var id string
			if err := json.Unmarshal(row[0], &id); err != nil || strings.TrimSpace(id) == "" {
				return "", &cliErrors.Error{Code: "invalid_response", Message: "tasks folder response contained an invalid id"}
			}
			return id, nil
		}
	}
	return "", nil
}

// taskRawFields decodes the fields shared by the tasks get/create/update responses.
// id and folder_id come back as JSON numbers from tasks.get (unlike tasks.list, which
// requests them via explicit column codes), so they are decoded via json.Number.
type taskRawFields struct {
	ID       json.Number `json:"id"`
	FolderID json.Number `json:"folder_id"`
	Title    *string     `json:"title"`
	Note     string      `json:"note"`
	Status   *int        `json:"status"`
	Priority int         `json:"priority"`
	EndDate  *int64      `json:"end_date"`
}

type taskDetail struct {
	ID        string `json:"id"`
	FolderID  string `json:"folder_id"`
	Title     string `json:"title"`
	Note      string `json:"note"`
	Status    *int   `json:"status"`
	Priority  int    `json:"priority"`
	EndDate   *int64 `json:"end_date"`
	Timestamp int64  `json:"timestamp"`
}

func runTasksGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("tasks.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	folder := f.String("folder")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		discovered, err := discoverTasksFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		if discovered == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "no standard tasks folder found; pass --folder explicitly"})
		}
		folder = discovered
	}

	params := url.Values{"id": {id}, "folder": {folder}}
	data, timestamp, err := c.DoWithTimestamp(ctx, http.MethodGet, "/tasks", "get", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var raw taskRawFields
	if err := json.Unmarshal(data, &raw); err != nil || raw.ID.String() == "" || raw.ID.String() != id ||
		raw.FolderID.String() == "" || raw.FolderID.String() != folder || raw.Title == nil ||
		(raw.Status != nil && (*raw.Status < 1 || *raw.Status > 5)) ||
		raw.Priority < 0 || raw.Priority > 3 || (raw.EndDate != nil && *raw.EndDate <= 0) {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode task response"})
	}

	detail := taskDetail{
		ID:        raw.ID.String(),
		FolderID:  raw.FolderID.String(),
		Title:     *raw.Title,
		Note:      raw.Note,
		Status:    raw.Status,
		Priority:  raw.Priority,
		EndDate:   raw.EndDate,
		Timestamp: timestamp,
	}

	if llm {
		render.CompactJSON(stdout, detail)
		return 0
	}

	endDate := ""
	if detail.EndDate != nil {
		endDate = strconv.FormatInt(*detail.EndDate, 10)
	}
	status := ""
	if detail.Status != nil {
		status = strconv.Itoa(*detail.Status)
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: detail.ID},
		{Name: "folder_id", Value: detail.FolderID},
		{Name: "title", Value: detail.Title},
		{Name: "note", Value: detail.Note},
		{Name: "status", Value: status},
		{Name: "priority", Value: strconv.Itoa(detail.Priority)},
		{Name: "end_date", Value: endDate},
		{Name: "timestamp", Value: strconv.FormatInt(detail.Timestamp, 10)},
	})
	return 0
}

// taskFieldRules are the parse-time checks for the task field flags of tasks create and
// update. They run in argument order, so the first bad value is the error reported.
func taskFieldRules() []flagRule {
	return []flagRule{
		intFlag("status", 1, 5, "--status must be an integer from 1 to 5"),
		intFlag("priority", 1, 3, "--priority must be an integer from 1 to 3"),
		checkedFlag("end-date", func(value string) *cliErrors.Error {
			_, err := parseTaskEndDate(value)
			return err
		}),
	}
}

// parseTaskEndDate converts an --end-date RFC3339 value to Unix epoch milliseconds.
func parseTaskEndDate(value string) (int64, *cliErrors.Error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return 0, invalidArgument("--end-date is not a valid RFC3339 timestamp: " + value)
	}
	ms := t.UTC().UnixMilli()
	if ms <= 0 {
		return 0, invalidArgument("--end-date must be after the Unix epoch")
	}
	return ms, nil
}

// taskFieldFlags holds the task field flags of tasks create and update. A nil field was
// not given, so update can tell "not provided" from "provided empty".
type taskFieldFlags struct {
	title    *string
	note     *string
	status   *int
	priority *int
	endDate  *int64
}

// readTaskFieldFlags reads the task field flags from flags parsed with taskFieldRules.
func readTaskFieldFlags(f commandFlags) taskFieldFlags {
	var fields taskFieldFlags
	if value, given := f.Lookup("title"); given {
		fields.title = &value
	}
	if value, given := f.Lookup("note"); given {
		fields.note = &value
	}
	if n, given := f.Int("status"); given {
		fields.status = &n
	}
	if n, given := f.Int("priority"); given {
		fields.priority = &n
	}
	if value, given := f.Lookup("end-date"); given {
		ms, _ := parseTaskEndDate(value) // already checked by taskFieldRules
		fields.endDate = &ms
	}
	return fields
}

func runTasksCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("tasks.create", args, taskFieldRules()...)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	fields := readTaskFieldFlags(f)

	if fields.title == nil || *fields.title == "" {
		return exitWithError(stderr, llm, invalidArgument("--title is required"))
	}

	folder := f.String("folder")
	body := map[string]any{"folder_id": folder, "title": *fields.title}
	if fields.note != nil {
		body["note"] = *fields.note
	}
	if fields.status != nil {
		body["status"] = *fields.status
	}
	if fields.priority != nil {
		body["priority"] = *fields.priority
	}
	if fields.endDate != nil {
		body["end_date"] = *fields.endDate
	}

	if f.Bool("dry-run") {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun      bool           `json:"dry_run"`
				WouldCreate map[string]any `json:"would_create"`
			}{DryRun: true, WouldCreate: body})
			return 0
		}
		fmt.Fprintf(stdout, "Dry run: would create task %q in folder %s\n", *fields.title, folder)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		discovered, err := discoverTasksFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		if discovered == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "no standard tasks folder found; pass --folder explicitly"})
		}
		folder = discovered
	}
	body["folder_id"] = folder

	data, timestamp, err := c.DoJSONWithTimestamp(ctx, http.MethodPut, "/tasks", "new", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var created struct {
		ID *json.Number `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == nil || strings.TrimSpace(created.ID.String()) == "" {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode create-task response"})
	}
	id := created.ID.String()
	parsedID, idErr := strconv.ParseInt(id, 10, 64)
	if idErr != nil || parsedID <= 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode create-task response"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID        string `json:"id"`
			FolderID  string `json:"folder_id"`
			Timestamp int64  `json:"timestamp"`
		}{ID: id, FolderID: folder, Timestamp: timestamp})
		return 0
	}

	fmt.Fprintf(stdout, "Created task %s in folder %s (timestamp %d)\n", id, folder, timestamp)
	return 0
}

func runTasksUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("tasks.update", args, taskFieldRules()...)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	timeRaw := f.String("timestamp")
	fields := readTaskFieldFlags(f)

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if timeRaw == "" {
		return exitWithError(stderr, llm, invalidArgument("--timestamp is required (from a prior tasks.get or tasks.create)"))
	}
	if timestamp, convErr := strconv.ParseInt(timeRaw, 10, 64); convErr != nil || timestamp <= 0 {
		return exitWithError(stderr, llm, invalidArgument("--timestamp must be a positive integer"))
	}
	if fields.title == nil && fields.note == nil && fields.status == nil && fields.priority == nil && fields.endDate == nil {
		return exitWithError(stderr, llm, invalidArgument("at least one of --title, --note, --status, --priority, --end-date is required"))
	}

	folder := f.String("folder")
	body := map[string]any{}
	if fields.title != nil {
		body["title"] = *fields.title
	}
	if fields.note != nil {
		body["note"] = *fields.note
	}
	if fields.status != nil {
		body["status"] = *fields.status
	}
	if fields.priority != nil {
		body["priority"] = *fields.priority
	}
	if fields.endDate != nil {
		body["end_date"] = *fields.endDate
	}

	if f.Bool("dry-run") {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun      bool           `json:"dry_run"`
				ID          string         `json:"id"`
				WouldUpdate map[string]any `json:"would_update"`
			}{DryRun: true, ID: id, WouldUpdate: body})
			return 0
		}
		fmt.Fprintf(stdout, "Dry run: would update task %s in folder %s\n", id, folder)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		discovered, err := discoverTasksFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		if discovered == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "no standard tasks folder found; pass --folder explicitly"})
		}
		folder = discovered
	}

	params := url.Values{"id": {id}, "folder": {folder}, "timestamp": {timeRaw}}
	_, newTimestamp, err := c.DoJSONWithTimestamp(ctx, http.MethodPut, "/tasks", "update", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID        string `json:"id"`
			Updated   bool   `json:"updated"`
			Timestamp int64  `json:"timestamp"`
		}{ID: id, Updated: true, Timestamp: newTimestamp})
		return 0
	}

	fmt.Fprintf(stdout, "Updated task %s (timestamp %d)\n", id, newTimestamp)
	return 0
}

func runTasksDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("tasks.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	timeRaw := f.String("timestamp")
	confirm := f.String("confirm")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if timeRaw == "" {
		return exitWithError(stderr, llm, invalidArgument("--timestamp is required (from a prior tasks.get)"))
	}
	if timestamp, convErr := strconv.ParseInt(timeRaw, 10, 64); convErr != nil || timestamp <= 0 {
		return exitWithError(stderr, llm, invalidArgument("--timestamp must be a positive integer"))
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("delete is destructive; pass --confirm <id> repeating the exact task id to delete"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm must exactly match --id; refusing to delete task "+id))
	}

	// --dry-run is checked before any authentication or network call: the confirmation
	// safeguard and dry-run validation are both purely local.
	if f.Bool("dry-run") {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool   `json:"dry_run"`
				ID     string `json:"id"`
			}{DryRun: true, ID: id})
			return 0
		}
		fmt.Fprintf(stdout, "Dry run: would delete task %s\n", id)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	folder := f.String("folder")
	if folder == "" {
		discovered, err := discoverTasksFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		if discovered == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "no standard tasks folder found; pass --folder explicitly"})
		}
		folder = discovered
	}

	params := url.Values{"timestamp": {timeRaw}}
	body := []map[string]any{{"id": id, "folder": folder}}
	data, err := c.DoJSON(ctx, http.MethodPut, "/tasks", "delete", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if refused := deleteRefusalError(data, "task "+id); refused != nil {
		return exitWithError(stderr, llm, refused)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID      string `json:"id"`
			Deleted bool   `json:"deleted"`
		}{ID: id, Deleted: true})
		return 0
	}

	fmt.Fprintf(stdout, "Deleted task %s\n", id)
	return 0
}
