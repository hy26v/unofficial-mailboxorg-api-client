// ABOUTME: The "contacts" command: list/get/create/update/delete against the authenticated
// ABOUTME: user's contacts folder, including structured input and optimistic locking.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	contactsDefaultLimit = 50
	contactsMaxLimit     = 500
)

// contactColumns are the OX contact field codes requested from the contacts.all call,
// in the order the response rows carry them: object id, display name, first name,
// last name, primary email.
const contactColumns = "1,500,501,502,555"

func init() {
	registerResource("contacts", runContacts)
	operations.Register(operations.Operation{
		Name:       "contacts.list",
		Resource:   "contacts",
		Action:     "list",
		Summary:    "List contacts in the authenticated user's default contacts folder",
		Keywords:   []string{"address book", "people", "phone", "email address"},
		Method:     http.MethodGet,
		Endpoint:   "/contacts",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"limit": {Type: "int", Required: false, Description: "max contacts to return (default 50, max 500)"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "display_name", "first_name", "last_name", "email"},
			ScalarFields: []string{"id", "display_name", "first_name", "last_name", "email"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "contacts.get",
		Resource:   "contacts",
		Action:     "get",
		Summary:    "Get one contact by id",
		Method:     http.MethodGet,
		Endpoint:   "/contacts",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":     {Type: "string", Required: true, Description: "the contact's id"},
			"folder": {Type: "string", Required: false, Description: "folder id; defaults to the account's default contacts folder"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "display_name", "first_name", "last_name", "email", "folder_id", "last_modified"},
			ScalarFields: []string{"id", "display_name", "first_name", "last_name", "email", "folder_id", "last_modified"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "contacts.create",
		Resource:   "contacts",
		Action:     "create",
		Summary:    "Create a contact from --field flags and/or structured --input",
		Keywords:   []string{"address book", "person"},
		Method:     http.MethodPut,
		Endpoint:   "/contacts",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"field":   {Type: "string", Required: false, Description: "repeatable name=value contact field, e.g. --field display_name=\"A B\" (overrides --input on conflict)"},
			"input":   {Type: "string", Required: false, Description: "structured input: a JSON object, or @<path> to read one from a file"},
			"folder":  {Type: "string", Required: false, Description: "folder id; defaults to the account's default contacts folder"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be sent without making a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "timestamp"},
			ScalarFields: []string{"id", "folder_id", "timestamp"},
			DryRunFields: []string{"dry_run", "action", "folder_id", "fields"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "contacts.update",
		Resource:   "contacts",
		Action:     "update",
		Summary:    "Partially update a contact, using --timestamp for optimistic locking",
		Method:     http.MethodPut,
		Endpoint:   "/contacts",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":        {Type: "string", Required: true, Description: "the contact's id"},
			"timestamp": {Type: "int", Required: true, Description: "last known modification time (epoch ms), for optimistic locking: last_modified from contacts.get, or timestamp from contacts.create/update"},
			"field":     {Type: "string", Required: false, Description: "repeatable name=value contact field to change (overrides --input on conflict)"},
			"input":     {Type: "string", Required: false, Description: "structured input: a JSON object, or @<path> to read one from a file"},
			"folder":    {Type: "string", Required: false, Description: "folder id; defaults to the account's default contacts folder"},
			"dry-run":   {Type: "bool", Required: false, Description: "validate and show what would be sent without making a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "timestamp", "updated_fields"},
			ScalarFields: []string{"id", "folder_id", "timestamp"},
			DryRunFields: []string{"dry_run", "action", "folder_id", "fields"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "contacts.delete",
		Resource:    "contacts",
		Action:      "delete",
		Summary:     "Delete a contact (destructive; requires --confirm <id> and --timestamp)",
		Method:      http.MethodPut,
		Endpoint:    "/contacts",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":        {Type: "string", Required: true, Description: "the contact's id"},
			"folder":    {Type: "string", Required: false, Description: "folder id; defaults to the account's default contacts folder"},
			"timestamp": {Type: "int", Required: true, Description: "last known modification time (epoch ms), for optimistic locking: last_modified from contacts.get, or timestamp from contacts.create/update"},
			"confirm":   {Type: "string", Required: true, Description: "must exactly repeat --id to confirm deletion"},
			"dry-run":   {Type: "bool", Required: false, Description: "validate and show what would be sent without making a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "deleted"},
			ScalarFields: []string{"id", "folder_id", "deleted"},
			DryRunFields: []string{"dry_run", "action", "folder_id", "fields"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "contacts.export",
		Resource:   "contacts",
		Action:     "export",
		Summary:    "Export a contacts folder as vCard text",
		Method:     http.MethodGet,
		Endpoint:   "/export",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"folder": {Type: "string", Required: false, Description: "folder id; defaults to the account's default contacts folder"},
			"output": {Type: "string", Required: false, Description: "write vCard text to this file instead of stdout"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"saved_to", "bytes_written"},
			ScalarFields: []string{"saved_to", "bytes_written"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "contacts.import",
		Resource:   "contacts",
		Action:     "import",
		Summary:    "Import a vCard file as one or more contacts",
		Method:     http.MethodPost,
		Endpoint:   "/import",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"file":    {Type: "string", Required: true, Description: "path to a local .vcf file to import"},
			"folder":  {Type: "string", Required: false, Description: "folder id; defaults to the account's default contacts folder"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be sent without making a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "folder_id", "items", "count", "failed", "failed_count"},
			ScalarFields: []string{"id", "folder_id", "count", "failed_count"},
			DryRunFields: []string{"dry_run", "folder_id", "filename"},
		},
	})
}

func runContacts(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("contacts requires an action, e.g. \"contacts list\""))
	}

	switch args[0] {
	case "list":
		return runContactsList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runContactsGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runContactsCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runContactsUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runContactsDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "export":
		return runContactsExport(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "import":
		return runContactsImport(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown contacts action: "+args[0]))
	}
}

type contact struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	Email       string `json:"email"`
}

func runContactsList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("contacts.list", args, checkedFlag("limit", checkContactsLimit))
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	limit := contactsDefaultLimit
	if value, given := f.Lookup("limit"); given {
		limit, _ = strconv.Atoi(value) // checkContactsLimit accepted it
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	folderID, err := defaultContactsFolder(ctx, c, sess)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	params := url.Values{
		"folder":          {folderID},
		"columns":         {contactColumns},
		"left_hand_limit": {"0"},
		// One row past the caller's limit: the range is applied server-side, so a response of
		// exactly limit rows is otherwise indistinguishable from a folder that holds exactly
		// that many contacts. The extra row is dropped before rendering.
		"right_hand_limit": {strconv.Itoa(limit + 1)},
	}
	data, err := c.Do(ctx, http.MethodGet, "/contacts", "all", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode contacts response"})
	}

	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}

	contacts := make([]contact, 0, len(rows))
	for i, row := range rows {
		ct, rowErr := rowToContact(row)
		if rowErr != nil {
			cerr := &cliErrors.Error{
				Code:    "invalid_response",
				Message: fmt.Sprintf("could not decode contacts row %d: %v", i+1, rowErr),
			}
			return exitWithError(stderr, llm, cerr)
		}
		contacts = append(contacts, ct)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Contacts  []contact `json:"contacts"`
			Truncated bool      `json:"truncated"`
		}{Contacts: contacts, Truncated: truncated})
		return 0
	}

	tableRows := make([][]string, len(contacts))
	for i, ct := range contacts {
		tableRows[i] = []string{ct.ID, ct.DisplayName, ct.Email}
	}
	render.Table(stdout, []string{"id", "display_name", "email"}, tableRows)
	writeTruncationNotice(stdout, truncated, limit)
	return 0
}

// checkContactsLimit accepts a contacts list --limit from 1 to contactsMaxLimit. It keeps
// the two messages of the old loop: one for a value that is not an integer and one for an
// integer out of range.
func checkContactsLimit(value string) *cliErrors.Error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return invalidArgument("--limit must be an integer")
	}
	if n < 1 || n > contactsMaxLimit {
		return invalidArgument(fmt.Sprintf("--limit must be between 1 and %d", contactsMaxLimit))
	}
	return nil
}

// defaultContactsFolder returns the authenticated user's default contacts folder id,
// taken from user.get's folder_id field (the folder that owns the user's own contact
// record), since no separate folder-discovery operation is available yet.
func defaultContactsFolder(ctx context.Context, c *client.Client, sess client.Session) (string, error) {
	data, err := c.Do(ctx, http.MethodGet, "/user", "get", sess, nil)
	if err != nil {
		return "", err
	}
	var user struct {
		FolderID string `json:"folder_id"`
	}
	if err := json.Unmarshal(data, &user); err != nil {
		return "", &cliErrors.Error{Code: "invalid_response", Message: "could not decode user response"}
	}
	if user.FolderID == "" {
		return "", &cliErrors.Error{Code: "invalid_response", Message: "user response did not include folder_id"}
	}
	return user.FolderID, nil
}

func rowToContact(row []json.RawMessage) (contact, error) {
	if len(row) != 5 {
		return contact{}, fmt.Errorf("got %d columns, want 5", len(row))
	}
	var ct contact
	fields := []*string{&ct.ID, &ct.DisplayName, &ct.FirstName, &ct.LastName, &ct.Email}
	for i, dst := range fields {
		if err := json.Unmarshal(row[i], dst); err != nil {
			return contact{}, fmt.Errorf("column %d is not a string", i+1)
		}
	}
	if ct.ID == "" {
		return contact{}, fmt.Errorf("contact id is empty")
	}
	return ct, nil
}

type contactDetail struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
	Email        string `json:"email"`
	FolderID     string `json:"folder_id"`
	LastModified int64  `json:"last_modified"`
}

func runContactsGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("contacts.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	folder := f.String("folder")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 5"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		var err error
		folder, err = defaultContactsFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
	}

	ct, cerr := fetchContact(ctx, c, sess, id, folder)
	if cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	if llm {
		render.CompactJSON(stdout, ct)
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: ct.ID},
		{Name: "display_name", Value: ct.DisplayName},
		{Name: "first_name", Value: ct.FirstName},
		{Name: "last_name", Value: ct.LastName},
		{Name: "email", Value: ct.Email},
		{Name: "folder_id", Value: ct.FolderID},
		{Name: "last_modified", Value: strconv.FormatInt(ct.LastModified, 10)},
	})
	return 0
}

// fetchContact fetches one contact by id and folder via GET /contacts?action=get.
func fetchContact(ctx context.Context, c *client.Client, sess client.Session, id, folder string) (contactDetail, *cliErrors.Error) {
	params := url.Values{"id": {id}, "folder": {folder}}
	data, err := c.Do(ctx, http.MethodGet, "/contacts", "get", sess, params)
	if err != nil {
		return contactDetail{}, toCLIError(err, "api_error")
	}

	var raw struct {
		ID           string `json:"id"`
		DisplayName  string `json:"display_name"`
		FirstName    string `json:"first_name"`
		LastName     string `json:"last_name"`
		Email1       string `json:"email1"`
		FolderID     string `json:"folder_id"`
		LastModified int64  `json:"last_modified"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || strings.TrimSpace(raw.ID) == "" || raw.ID != id ||
		strings.TrimSpace(raw.FolderID) == "" || raw.FolderID != folder || raw.LastModified <= 0 {
		return contactDetail{}, &cliErrors.Error{Code: "invalid_response", Message: "could not decode contact response"}
	}

	return contactDetail{
		ID:           raw.ID,
		DisplayName:  raw.DisplayName,
		FirstName:    raw.FirstName,
		LastName:     raw.LastName,
		Email:        raw.Email1,
		FolderID:     raw.FolderID,
		LastModified: raw.LastModified,
	}, nil
}

// parseFieldFlag splits a "--field name=value" argument into its name and value. It
// returns an error if there is no "=".
func parseFieldFlag(raw string) (string, string, error) {
	name, value, ok := strings.Cut(raw, "=")
	if !ok || name == "" {
		return "", "", fmt.Errorf("--field must be name=value, got: %s", raw)
	}
	return name, value, nil
}

// parseInputValue parses a --input value into a JSON object. A value starting with "@"
// is read as a file path; any other value is parsed as a literal JSON object. Reading
// from stdin (--input -) is not supported: the CLI's Handler signature has no injected
// stdin, so this is deferred rather than reading the process's real stdin directly.
func parseInputValue(raw string) (map[string]any, error) {
	var contents []byte
	if strings.HasPrefix(raw, "@") {
		path := strings.TrimPrefix(raw, "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("could not read --input file %s: %w", path, err)
		}
		contents = data
	} else {
		contents = []byte(raw)
	}

	var m map[string]any
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	if err := decoder.Decode(&m); err != nil {
		return nil, fmt.Errorf("--input is not a valid JSON object: %w", err)
	}
	if m == nil {
		return nil, fmt.Errorf("--input is not a valid JSON object: got null")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("--input is not a valid JSON object: multiple JSON values")
		}
		return nil, fmt.Errorf("--input is not a valid JSON object: %w", err)
	}
	return m, nil
}

// contactCreateFolder reconciles the two ways a caller can say which folder a new contact
// belongs to: the --folder flag, and folder_id supplied as ordinary contact data through
// --field or --input. folder_id is a normal OX contact field, so both spellings are natural.
// Either one alone decides the folder; two different values are a contradiction, because
// honouring one means filing the contact somewhere the caller also said it should not go. An
// empty result means neither was given and the account default applies.
func contactCreateFolder(flagFolder string, fields map[string]any) (string, *cliErrors.Error) {
	raw, ok := fields["folder_id"]
	if !ok {
		return flagFolder, nil
	}
	if raw == nil {
		// JSON null names no folder; treat it as absent instead of the text "<nil>".
		delete(fields, "folder_id")
		return flagFolder, nil
	}
	fieldFolder := contactFieldString(raw)
	if flagFolder == "" {
		return fieldFolder, nil
	}
	if fieldFolder != flagFolder {
		return "", &cliErrors.Error{
			Code: "invalid_argument",
			Message: fmt.Sprintf(
				"--folder %s and folder_id %s name different folders; give the folder once",
				flagFolder, fieldFolder,
			),
		}
	}
	return flagFolder, nil
}

// checkContactUpdateFolder rejects a folder_id in contacts update fields that could move
// the contact. folder_id is the target folder's selector for update, not an editable field:
// the server would file the contact elsewhere while the output reported the old folder.
// A null folder_id, or one equal to --folder, carries no change and is removed from the
// body. Without --folder the target is resolved only at run time, so a folder_id is
// refused before any request, which keeps --dry-run and the real run consistent.
func checkContactUpdateFolder(targetFolder string, fields map[string]any) *cliErrors.Error {
	raw, ok := fields["folder_id"]
	if !ok {
		return nil
	}
	if raw == nil {
		delete(fields, "folder_id")
		return nil
	}
	fieldFolder := contactFieldString(raw)
	if targetFolder != "" && fieldFolder == targetFolder {
		delete(fields, "folder_id")
		return nil
	}
	message := fmt.Sprintf("folder_id %s differs from the target folder %s; contacts update does not support moving a contact to another folder", fieldFolder, targetFolder)
	if targetFolder == "" {
		message = fmt.Sprintf("folder_id %s needs --folder with the same value; contacts update selects the contact's current folder with --folder and does not support moving a contact to another folder", fieldFolder)
	}
	return &cliErrors.Error{Code: "invalid_argument", Message: message}
}

// contactFieldString renders one contact field value as the string the OX folder parameters
// use. json.Number values are kept exact so large numeric folder ids are not rounded.
func contactFieldString(value any) string {
	if number, ok := value.(json.Number); ok {
		return number.String()
	}
	if number, ok := value.(float64); ok {
		return strconv.FormatFloat(number, 'f', -1, 64)
	}
	return fmt.Sprint(value)
}

// buildContactFields merges structured --input with --field flags into one JSON body,
// with --field values overriding --input on key conflicts.
func buildContactFields(inputValue string, fieldFlags []string) (map[string]any, error) {
	fields := map[string]any{}
	if inputValue != "" {
		parsed, err := parseInputValue(inputValue)
		if err != nil {
			return nil, err
		}
		for k, v := range parsed {
			fields[k] = v
		}
	}
	for _, raw := range fieldFlags {
		name, value, err := parseFieldFlag(raw)
		if err != nil {
			return nil, err
		}
		fields[name] = value
	}
	return fields, nil
}

func runContactsCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("contacts.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	folder := f.String("folder")
	inputValue := f.String("input")
	fieldFlags := f.Values("field")

	if inputValue == "" && len(fieldFlags) == 0 {
		return exitWithError(stderr, llm, invalidArgument("contacts create requires --field name=value and/or --input"))
	}

	fields, buildErr := buildContactFields(inputValue, fieldFlags)
	if buildErr != nil {
		return exitWithError(stderr, llm, invalidArgument(buildErr.Error()))
	}

	folder, folderErr := contactCreateFolder(folder, fields)
	if folderErr != nil {
		return exitWithError(stderr, llm, folderErr)
	}

	if dryRun {
		renderContactDryRun(stdout, llm, "create", folder, fields)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		var err error
		folder, err = defaultContactsFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
	}
	fields["folder_id"] = folder

	data, timestamp, err := c.DoJSONWithTimestamp(ctx, http.MethodPut, "/contacts", "new", sess, nil, fields)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var created struct {
		ID *string `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == nil || strings.TrimSpace(*created.ID) == "" {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode contacts create response"})
	}
	id := *created.ID

	result := struct {
		ID        string `json:"id"`
		FolderID  string `json:"folder_id"`
		Timestamp int64  `json:"timestamp"`
	}{ID: id, FolderID: folder, Timestamp: timestamp}

	if llm {
		render.CompactJSON(stdout, result)
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: result.ID},
		{Name: "folder_id", Value: result.FolderID},
		{Name: "timestamp", Value: strconv.FormatInt(result.Timestamp, 10)},
	})
	return 0
}

func runContactsUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("contacts.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	folder := f.String("folder")
	timestampStr := f.String("timestamp")
	inputValue := f.String("input")
	fieldFlags := f.Values("field")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 5"))
	}
	if timestampStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--timestamp is required for optimistic locking; use last_modified from contacts.get, or timestamp from contacts.create/update"))
	}
	timestamp, convErr := strconv.ParseInt(timestampStr, 10, 64)
	if convErr != nil || timestamp <= 0 {
		return exitWithError(stderr, llm, invalidArgument("--timestamp must be a positive integer, got: "+timestampStr))
	}
	if inputValue == "" && len(fieldFlags) == 0 {
		return exitWithError(stderr, llm, invalidArgument("contacts update requires --field name=value and/or --input"))
	}

	fields, buildErr := buildContactFields(inputValue, fieldFlags)
	if buildErr != nil {
		return exitWithError(stderr, llm, invalidArgument(buildErr.Error()))
	}

	if folderErr := checkContactUpdateFolder(folder, fields); folderErr != nil {
		return exitWithError(stderr, llm, folderErr)
	}

	if dryRun {
		renderContactDryRun(stdout, llm, "update", folder, fields)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		var err error
		folder, err = defaultContactsFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
	}

	params := url.Values{
		"id":        {id},
		"folder":    {folder},
		"timestamp": {strconv.FormatInt(timestamp, 10)},
	}
	_, newTimestamp, err := c.DoJSONWithTimestamp(ctx, http.MethodPut, "/contacts", "update", sess, params, fields)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID            string         `json:"id"`
			FolderID      string         `json:"folder_id"`
			Timestamp     int64          `json:"timestamp"`
			UpdatedFields map[string]any `json:"updated_fields"`
		}{ID: id, FolderID: folder, Timestamp: newTimestamp, UpdatedFields: fields})
		return 0
	}

	fieldsJSON, _ := json.Marshal(fields)
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: id},
		{Name: "folder_id", Value: folder},
		{Name: "timestamp", Value: strconv.FormatInt(newTimestamp, 10)},
		{Name: "updated_fields", Value: string(fieldsJSON)},
	})
	return 0
}

func runContactsDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("contacts.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	folder := f.String("folder")
	timestampStr := f.String("timestamp")
	confirm := f.String("confirm")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 5"))
	}
	if timestampStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--timestamp is required for optimistic locking; use last_modified from contacts.get, or timestamp from contacts.create/update"))
	}
	timestamp, convErr := strconv.ParseInt(timestampStr, 10, 64)
	if convErr != nil || timestamp <= 0 {
		return exitWithError(stderr, llm, invalidArgument("--timestamp must be a positive integer, got: "+timestampStr))
	}
	// This is a destructive operation: --confirm must exactly repeat --id before any
	// network call is made, and there is no bare --yes shortcut.
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("deleting a contact requires --confirm <id>, repeating --id "+id+" exactly"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm "+confirm+" does not match --id "+id+"; refusing to delete"))
	}

	if dryRun {
		renderContactDryRun(stdout, llm, "delete", folder, map[string]any{"id": id, "timestamp": timestamp})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		var err error
		folder, err = defaultContactsFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
	}

	params := url.Values{"timestamp": {strconv.FormatInt(timestamp, 10)}}
	body := []map[string]string{{"id": id, "folder": folder}}
	data, err := c.DoJSON(ctx, http.MethodPut, "/contacts", "delete", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if refused := deleteRefusalError(data, "contact "+id); refused != nil {
		return exitWithError(stderr, llm, refused)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID       string `json:"id"`
			FolderID string `json:"folder_id"`
			Deleted  bool   `json:"deleted"`
		}{ID: id, FolderID: folder, Deleted: true})
		return 0
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: id},
		{Name: "folder_id", Value: folder},
		{Name: "deleted", Value: "true"},
	})
	return 0
}

// renderContactDryRun prints what a create/update/delete would send without making a
// request. folder may be empty if it will be resolved from the account at execution time.
func renderContactDryRun(stdout io.Writer, llm bool, action, folder string, fields map[string]any) {
	folderDisplay := folder
	if folderDisplay == "" {
		folderDisplay = "(resolved from account default at run time)"
	}
	if llm {
		render.CompactJSON(stdout, struct {
			DryRun bool           `json:"dry_run"`
			Action string         `json:"action"`
			Folder string         `json:"folder_id"`
			Fields map[string]any `json:"fields"`
		}{DryRun: true, Action: action, Folder: folderDisplay, Fields: fields})
		return
	}
	fieldsJSON, _ := json.Marshal(fields)
	render.KeyValueTable(stdout, []render.Field{
		{Name: "dry_run", Value: "true"},
		{Name: "action", Value: action},
		{Name: "folder_id", Value: folderDisplay},
		{Name: "fields", Value: string(fieldsJSON)},
	})
}

func runContactsExport(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("contacts.export", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	folder := f.String("folder")
	output := f.String("output")

	if cerr := requireOutputFileForDocument(llm, output, "vCard"); cerr != nil {
		return exitWithError(stderr, llm, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		var err error
		folder, err = defaultContactsFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
	}

	content, err := c.DoRaw(ctx, http.MethodGet, "/export", "VCARD", sess, url.Values{"folder": {folder}})
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

func runContactsImport(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("contacts.import", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	path := f.String("file")
	folder := f.String("folder")

	if path == "" {
		return exitWithError(stderr, llm, invalidArgument("--file is required, e.g. --file contacts.vcf"))
	}
	info, statErr := os.Stat(path)
	if statErr != nil || !info.Mode().IsRegular() {
		return exitWithError(stderr, llm, invalidArgument("--file does not exist or is not a regular readable file: "+path))
	}

	if dryRun {
		folderDisplay := folder
		if folderDisplay == "" {
			folderDisplay = "(resolved from account default at run time)"
		}
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun   bool   `json:"dry_run"`
				Folder   string `json:"folder_id"`
				Filename string `json:"filename"`
			}{DryRun: true, Folder: folderDisplay, Filename: filepath.Base(path)})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "folder_id", Value: folderDisplay},
			{Name: "filename", Value: filepath.Base(path)},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	if folder == "" {
		var err error
		folder, err = defaultContactsFolder(ctx, c, sess)
		if err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
	}

	file, openErr := os.Open(path)
	if openErr != nil {
		return exitWithError(stderr, llm, invalidArgument("could not open --file: "+openErr.Error()))
	}
	defer file.Close()

	params := url.Values{"folder": {folder}}
	data, err := c.DoMultipart(ctx, "/import", "VCARD", sess, params, struct{}{}, filepath.Base(path), "text/vcard", file)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var results []importResult
	if err := json.Unmarshal(data, &results); err != nil || len(results) == 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode contacts import response"})
	}
	return emitImportOutput(stdout, stderr, llm, results, folder, "contacts")
}
