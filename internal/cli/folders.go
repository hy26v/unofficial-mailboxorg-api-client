// ABOUTME: The "folders" commands: list/get/create/ensure/update/delete folders in the private
// ABOUTME: folder tree via the OX middleware's /folders module.
//
// folders.subscribe and folders.unsubscribe are not implemented. Live verification against
// the test deployment (dedicated test account, using a throwaway subfolder created and
// deleted for the test) confirmed:
//   - GET/PUT /folders?action=unsubscribe (with id= or root=, with or without tree=0)
//     returns {"code":"SVL-0001","error_desc":"Unknown AJAX action."}, byte-identical to
//     the response for a deliberately nonexistent action used as a control. The action is
//     not implemented on this deployment.
//   - PUT /folders?action=subscribe (body [{"id":"<id>","subscribed":false}], tree=0)
//     validates its body shape but returns {"code":"FLD-1001","error_desc":"Unexpected
//     error: Subscribe not supported for known trees."} for the standard mail folder tree,
//     which is the only tree this account has.
//   - PUT /folders?action=update with {"subscribed":false} in the body is accepted (200,
//     no error) but is a silent no-op: a follow-up action=get still reports the folder's
//     original subscribed state.
//
// There is no request shape on this deployment that actually changes a mail folder's
// subscription state, so implementing these commands would mean either erroring on every
// call (misleading, since the HTTP requests themselves succeed) or silently doing nothing
// while reporting success. Neither is acceptable for a user-facing command.
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
	"strconv"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	// folderListColumns requests id, title, module, type, and subfolder presence, in
	// that order. The OX middleware returns one array per folder with values in this
	// column order.
	folderListColumns = "1,300,301,302,304"

	// The CLI exposes the virtual private tree, whose root id is "1". Mail mutations
	// use the legacy primary tree instead, where the primary account root is default0.
	privateFolderTree = "1"
	privateFolderRoot = "1"
	primaryMailRoot   = "default0"
)

func init() {
	registerResource("folders", runFolders)
	operations.Register(operations.Operation{
		Name:       "folders.list",
		Resource:   "folders",
		Action:     "list",
		Summary:    "List folders under a parent folder in the private folder tree",
		Keywords:   []string{"mailboxes", "directories", "inbox", "sent", "trash"},
		Method:     http.MethodGet,
		Endpoint:   "/folders",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"parent": {Type: "string", Required: false, Description: "parent folder id; defaults to \"1\" (the private folder root)"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "title", "module", "type", "has_subfolders"},
			ScalarFields: []string{"id", "title", "module", "type", "has_subfolders"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "folders.get",
		Resource:   "folders",
		Action:     "get",
		Summary:    "Get a single folder by id",
		Method:     http.MethodGet,
		Endpoint:   "/folders",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id": {Type: "string", Required: true, Description: "folder id"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "title", "module", "parent_id", "has_subfolders", "last_modified", "permissions"},
			ScalarFields: []string{"id", "title", "module", "parent_id", "has_subfolders", "last_modified"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "folders.create",
		Resource:   "folders",
		Action:     "create",
		Summary:    "Create a folder under a parent or at the top level",
		Method:     http.MethodPut,
		Endpoint:   "/folders",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"parent":    {Type: "string", Required: true, Description: "parent folder id; use \"1\" for a top-level folder in the private tree"},
			"title":     {Type: "string", Required: true, Description: "new folder title"},
			"module":    {Type: "string", Required: true, Description: "folder module, e.g. contacts, calendar, tasks, infostore, mail"},
			"dry-run":   {Type: "bool", Required: false, Description: "show the exact normalized request without authentication or mutation"},
			"preflight": {Type: "bool", Required: false, Description: "authenticate and validate the normalized parent without mutation"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "dry_run", "preflight", "valid", "requested_parent", "parent", "title", "module", "request"},
			ScalarFields: []string{"id", "dry_run", "preflight", "valid", "requested_parent", "parent", "title", "module"},
			DryRunFields: []string{"dry_run", "requested_parent", "parent", "title", "module", "request"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "folders.ensure",
		Resource:   "folders",
		Action:     "ensure",
		Summary:    "Return an exact matching child folder or create it once",
		Method:     http.MethodPut,
		Endpoint:   "/folders",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"parent":    {Type: "string", Required: true, Description: "parent folder id; use \"1\" for the private root"},
			"title":     {Type: "string", Required: true, Description: "exact child-folder title"},
			"module":    {Type: "string", Required: true, Description: "folder module"},
			"dry-run":   {Type: "bool", Required: false, Description: "show the normalized ensure plan without authentication"},
			"preflight": {Type: "bool", Required: false, Description: "check whether creation is needed without mutating"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "created", "would_create", "dry_run", "preflight", "requested_parent", "parent", "title", "module", "request"},
			ScalarFields: []string{"id", "created", "would_create", "dry_run", "preflight", "requested_parent", "parent", "title", "module"},
			DryRunFields: []string{"id", "created", "would_create", "dry_run", "requested_parent", "parent", "title", "module", "request"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "folders.update",
		Resource:   "folders",
		Action:     "update",
		Summary:    "Rename a folder and/or replace its permissions",
		Method:     http.MethodPut,
		Endpoint:   "/folders",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":          {Type: "string", Required: true, Description: "folder id"},
			"title":       {Type: "string", Required: false, Description: "new folder title; --title and/or --permissions must be given"},
			"permissions": {Type: "string", Required: false, Description: "JSON array replacing the folder's full permissions list, e.g. [{\"entity\":3,\"bits\":403710016,\"group\":false}]; or @file.json to read from a file"},
			"dry-run":     {Type: "bool", Required: false, Description: "validate only, do not send the update request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "id", "title", "permissions"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "folders.delete",
		Resource:    "folders",
		Action:      "delete",
		Summary:     "Delete a folder",
		Method:      http.MethodPut,
		Endpoint:    "/folders",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "string", Required: true, Description: "folder id"},
			"confirm": {Type: "string", Required: true, Description: "must exactly repeat the --id value to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate only, do not send the delete request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "id"},
			ScalarFields: []string{"deleted", "id"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
}

func runFolders(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("folders requires an action, e.g. \"folders list\""))
	}

	switch args[0] {
	case "list":
		return runFoldersList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runFoldersGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runFoldersCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "ensure":
		return runFoldersEnsure(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runFoldersUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runFoldersDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown folders action: "+args[0]))
	}
}

type folderListItem struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Module        string `json:"module"`
	Type          int    `json:"type"`
	HasSubfolders bool   `json:"has_subfolders"`
}

func runFoldersList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("folders.list", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	parent := privateFolderRoot
	if value, given := f.Lookup("parent"); given {
		parent = value
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	folders, err := listFolders(ctx, c, sess, parent)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Folders []folderListItem `json:"folders"`
		}{Folders: folders})
		return 0
	}

	tableRows := make([][]string, len(folders))
	for i, f := range folders {
		tableRows[i] = []string{f.ID, f.Title, f.Module, strconv.Itoa(f.Type), strconv.FormatBool(f.HasSubfolders)}
	}
	render.Table(stdout, []string{"ID", "TITLE", "MODULE", "TYPE", "SUBFOLDERS"}, tableRows)
	return 0
}

// folderPermission is one entry of a folder's permissions list, in the shape the OX
// middleware accepts on write and returns on read (confirmed live against
// GET /folders?action=get: {"entity":3,"bits":403710016,"group":false}, alongside a
// server-assigned "identifier" field that is read-only and not sent on write).
type folderPermission struct {
	Entity int  `json:"entity"`
	Bits   int  `json:"bits"`
	Group  bool `json:"group"`
}

type folderDetail struct {
	ID            string             `json:"id"`
	Title         string             `json:"title"`
	Module        string             `json:"module"`
	ParentID      string             `json:"parent_id"`
	HasSubfolders bool               `json:"has_subfolders"`
	LastModified  *int64             `json:"last_modified"`
	Permissions   []folderPermission `json:"permissions"`
}

type ojFolder struct {
	ID           string  `json:"id"`
	Title        *string `json:"title"`
	Module       *string `json:"module"`
	ParentID     *string `json:"folder_id"`
	Subfolders   *bool   `json:"subfolders"`
	LastModified *int64  `json:"last_modified"`
	// LastModifiedUTC is column 6, a UTC timestamp. last_modified (column 5) is a Time
	// value shifted to the user's time zone, so it is not used for optimistic locking.
	LastModifiedUTC *int64             `json:"last_modified_utc"`
	Permissions     []folderPermission `json:"permissions"`
}

func runFoldersGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("folders.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")
	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	detail, err := getFolder(ctx, c, sess, id)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, detail)
		return 0
	}

	fields := []render.Field{
		{Name: "id", Value: detail.ID},
		{Name: "title", Value: detail.Title},
		{Name: "module", Value: detail.Module},
		{Name: "parent_id", Value: detail.ParentID},
		{Name: "has_subfolders", Value: strconv.FormatBool(detail.HasSubfolders)},
	}
	if len(detail.Permissions) > 0 {
		permsJSON, _ := json.Marshal(detail.Permissions)
		fields = append(fields, render.Field{Name: "permissions", Value: string(permsJSON)})
	}
	render.KeyValueTable(stdout, fields)
	return 0
}

type folderCreateRequest struct {
	Method   string            `json:"method"`
	Endpoint string            `json:"endpoint"`
	Action   string            `json:"action"`
	Params   map[string]string `json:"params"`
	Body     map[string]any    `json:"body"`
}

type folderCreatePlan struct {
	RequestedParent string
	Parent          string
	Title           string
	Module          string
	Request         folderCreateRequest
}

type folderCreatePreview struct {
	DryRun          bool                `json:"dry_run,omitempty"`
	Preflight       bool                `json:"preflight,omitempty"`
	Valid           bool                `json:"valid,omitempty"`
	RequestedParent string              `json:"requested_parent"`
	Parent          string              `json:"parent"`
	Title           string              `json:"title"`
	Module          string              `json:"module"`
	Request         folderCreateRequest `json:"request"`
}

func newFolderCreatePlan(parent, title, module string) (folderCreatePlan, error) {
	parent = strings.TrimSpace(parent)
	title = strings.TrimSpace(title)
	module = strings.ToLower(strings.TrimSpace(module))
	if parent == "" || title == "" || module == "" {
		return folderCreatePlan{}, fmt.Errorf("parent, title, and module must not be blank")
	}
	requestParent := parent
	if module == "mail" && parent == privateFolderRoot {
		requestParent = primaryMailRoot
	}
	body := map[string]any{"folder": map[string]any{"title": title, "module": module}}
	request := folderCreateRequest{
		Method: http.MethodPut, Endpoint: "/folders", Action: "new",
		Params: map[string]string{"folder_id": requestParent}, Body: body,
	}
	return folderCreatePlan{RequestedParent: parent, Parent: requestParent, Title: title, Module: module, Request: request}, nil
}

func validateFolderCreateParent(ctx context.Context, c *client.Client, sess client.Session, plan folderCreatePlan) error {
	if plan.RequestedParent == privateFolderRoot {
		_, err := listFolders(ctx, c, sess, privateFolderRoot)
		return err
	}
	parent, err := getFolder(ctx, c, sess, plan.RequestedParent)
	if err != nil {
		return err
	}
	if parent.Module != plan.Module {
		return &cliErrors.Error{Code: "invalid_argument", Message: fmt.Sprintf("parent folder module is %q, not %q", parent.Module, plan.Module)}
	}
	return nil
}

func createFolder(ctx context.Context, c *client.Client, sess client.Session, plan folderCreatePlan) (string, error) {
	params := url.Values{"folder_id": {plan.Parent}}
	data, err := c.DoJSON(ctx, plan.Request.Method, plan.Request.Endpoint, plan.Request.Action, sess, params, plan.Request.Body)
	if err != nil {
		return "", err
	}
	var newID string
	if err := json.Unmarshal(data, &newID); err != nil || strings.TrimSpace(newID) == "" {
		return "", &cliErrors.Error{Code: "invalid_response", Message: "could not decode folders create response"}
	}
	return newID, nil
}

func renderFolderCreatePreview(w io.Writer, preview folderCreatePreview) {
	fields := []render.Field{}
	if preview.DryRun {
		fields = append(fields, render.Field{Name: "dry_run", Value: "true"})
	}
	if preview.Preflight {
		fields = append(fields, render.Field{Name: "preflight", Value: "true"}, render.Field{Name: "valid", Value: strconv.FormatBool(preview.Valid)})
	}
	fields = append(fields,
		render.Field{Name: "requested_parent", Value: preview.RequestedParent},
		render.Field{Name: "parent", Value: preview.Parent},
		render.Field{Name: "title", Value: preview.Title},
		render.Field{Name: "module", Value: preview.Module},
	)
	render.KeyValueTable(w, fields)
}

type folderEnsureResult struct {
	ID              *string              `json:"id"`
	Created         bool                 `json:"created"`
	WouldCreate     bool                 `json:"would_create,omitempty"`
	DryRun          bool                 `json:"dry_run,omitempty"`
	Preflight       bool                 `json:"preflight,omitempty"`
	RequestedParent string               `json:"requested_parent"`
	Parent          string               `json:"parent"`
	Title           string               `json:"title"`
	Module          string               `json:"module"`
	Request         *folderCreateRequest `json:"request,omitempty"`
}

func runFoldersEnsure(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("folders.ensure", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm, dryRun, preflight := f.LLM(), f.Bool("dry-run"), f.Bool("preflight")
	parent, title, module := f.String("parent"), f.String("title"), f.String("module")
	if dryRun && preflight {
		return exitWithError(stderr, llm, invalidArgument("--dry-run and --preflight are mutually exclusive"))
	}
	plan, err := newFolderCreatePlan(parent, title, module)
	if err != nil {
		return exitWithError(stderr, llm, invalidArgument(err.Error()))
	}
	result := folderEnsureResult{RequestedParent: plan.RequestedParent, Parent: plan.Parent, Title: plan.Title, Module: plan.Module}
	if dryRun {
		result.DryRun, result.WouldCreate, result.Request = true, true, &plan.Request
		renderFolderEnsureResult(stdout, llm, result)
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}
	validationCtx, validationCancel := context.WithTimeout(context.Background(), requestTimeout)
	if err := validateFolderCreateParent(validationCtx, c, sess, plan); err != nil {
		validationCancel()
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	validationCancel()

	// The exact-match lookup and create are one idempotent transaction. A process-local
	// mutex is insufficient because two CLI invocations can race, so serialize this section
	// with an owner-only lock beside the account's session state.
	if !preflight {
		lockCtx, lockCancel := context.WithTimeout(context.Background(), 2*requestTimeout)
		lock, lockErr := acquireExclusiveFileLock(lockCtx, sessionPath+".folders-ensure.lock")
		lockCancel()
		if lockErr != nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "internal_error", Message: "could not coordinate folders ensure: " + lockErr.Error()})
		}
		defer func() { _ = lock.release() }()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*requestTimeout)
	defer cancel()
	children, err := listFolders(ctx, c, sess, plan.RequestedParent)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	var matches []folderListItem
	for _, child := range children {
		if child.Title == plan.Title && child.Module == plan.Module {
			matches = append(matches, child)
		}
	}
	if len(matches) > 1 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("found %d exact child folders named %q with module %q under %s", len(matches), plan.Title, plan.Module, plan.RequestedParent)})
	}
	if len(matches) == 1 {
		result.ID = stringPointer(matches[0].ID)
		result.Preflight = preflight
		renderFolderEnsureResult(stdout, llm, result)
		return 0
	}
	if preflight {
		result.Preflight, result.WouldCreate, result.Request = true, true, &plan.Request
		renderFolderEnsureResult(stdout, llm, result)
		return 0
	}

	newID, err := createFolder(ctx, c, sess, plan)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	created, err := getFolder(ctx, c, sess, newID)
	if err != nil || created.Title != plan.Title || created.Module != plan.Module || created.ParentID != plan.RequestedParent {
		// The folder exists now, so the error names its id: the caller needs it to inspect
		// or remove the folder, and a retry would otherwise not know what was created.
		message := fmt.Sprintf("created folder %s could not be verified", newID)
		if err != nil {
			message += ": " + err.Error()
		}
		message += fmt.Sprintf("; inspect it with folders get --id %s, and remove it with folders delete --id %s --confirm %s if it is not wanted", newID, newID, newID)
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "verification_failed", Message: message})
	}
	result.ID, result.Created = stringPointer(newID), true
	renderFolderEnsureResult(stdout, llm, result)
	return 0
}

func renderFolderEnsureResult(w io.Writer, llm bool, result folderEnsureResult) {
	if llm {
		render.CompactJSON(w, result)
		return
	}
	id := ""
	if result.ID != nil {
		id = *result.ID
	}
	fields := []render.Field{{Name: "id", Value: id}, {Name: "created", Value: strconv.FormatBool(result.Created)}}
	if result.WouldCreate {
		fields = append(fields, render.Field{Name: "would_create", Value: "true"})
	}
	if result.DryRun {
		fields = append(fields, render.Field{Name: "dry_run", Value: "true"})
	}
	if result.Preflight {
		fields = append(fields, render.Field{Name: "preflight", Value: "true"})
	}
	fields = append(fields, render.Field{Name: "parent", Value: result.Parent}, render.Field{Name: "title", Value: result.Title}, render.Field{Name: "module", Value: result.Module})
	render.KeyValueTable(w, fields)
}

func runFoldersCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("folders.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm, dryRun, preflight := f.LLM(), f.Bool("dry-run"), f.Bool("preflight")
	parent, title, module := f.String("parent"), f.String("title"), f.String("module")
	if strings.TrimSpace(parent) == "" {
		return exitWithError(stderr, llm, invalidArgument("--parent is required"))
	}
	if strings.TrimSpace(title) == "" {
		return exitWithError(stderr, llm, invalidArgument("--title is required"))
	}
	if strings.TrimSpace(module) == "" {
		return exitWithError(stderr, llm, invalidArgument("--module is required"))
	}
	if dryRun && preflight {
		return exitWithError(stderr, llm, invalidArgument("--dry-run and --preflight are mutually exclusive"))
	}
	plan, planErr := newFolderCreatePlan(parent, title, module)
	if planErr != nil {
		return exitWithError(stderr, llm, invalidArgument(planErr.Error()))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, folderCreatePreview{DryRun: true, RequestedParent: plan.RequestedParent, Parent: plan.Parent, Title: plan.Title, Module: plan.Module, Request: plan.Request})
		} else {
			renderFolderCreatePreview(stdout, folderCreatePreview{DryRun: true, RequestedParent: plan.RequestedParent, Parent: plan.Parent, Title: plan.Title, Module: plan.Module, Request: plan.Request})
		}
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	if preflight {
		if err := validateFolderCreateParent(ctx, c, sess, plan); err != nil {
			return exitWithError(stderr, llm, toCLIError(err, "api_error"))
		}
		preview := folderCreatePreview{Preflight: true, Valid: true, RequestedParent: plan.RequestedParent, Parent: plan.Parent, Title: plan.Title, Module: plan.Module, Request: plan.Request}
		if llm {
			render.CompactJSON(stdout, preview)
		} else {
			renderFolderCreatePreview(stdout, preview)
		}
		return 0
	}

	newID, err := createFolder(ctx, c, sess, plan)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID string `json:"id"`
		}{ID: newID})
		return 0
	}
	fmt.Fprintf(stdout, "id\t%s\n", newID)
	return 0
}

func runFoldersUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("folders.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm, dryRun := f.LLM(), f.Bool("dry-run")
	id, title, permissionsRaw := f.String("id"), f.String("title"), f.String("permissions")
	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if title == "" && permissionsRaw == "" {
		return exitWithError(stderr, llm, invalidArgument("folders update requires --title and/or --permissions"))
	}

	var permissions []folderPermission
	if permissionsRaw != "" {
		parsed, perr := parsePermissionsFlag(permissionsRaw)
		if perr != nil {
			return exitWithError(stderr, llm, invalidArgument(perr.Error()))
		}
		permissions = parsed
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun      bool               `json:"dry_run"`
				ID          string             `json:"id"`
				Title       string             `json:"title,omitempty"`
				Permissions []folderPermission `json:"permissions,omitempty"`
			}{DryRun: true, ID: id, Title: title, Permissions: permissions})
		} else {
			fmt.Fprintf(stdout, "dry_run: would update folder %s (title=%q, permissions=%d entries)\n", id, title, len(permissions))
		}
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	params, lockErr := folderLockParams(ctx, c, sess, id)
	if lockErr != nil {
		return exitWithError(stderr, llm, toCLIError(lockErr, "api_error"))
	}
	params.Set("id", id)
	folderFields := map[string]any{}
	if title != "" {
		folderFields["title"] = title
	}
	if permissionsRaw != "" {
		folderFields["permissions"] = permissions
	}
	body := map[string]any{"folder": folderFields}
	data, err := c.DoJSON(ctx, http.MethodPut, "/folders", "update", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var updatedID string
	if err := json.Unmarshal(data, &updatedID); err != nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode folders update response"})
	}
	if strings.TrimSpace(updatedID) == "" {
		updatedID = id
	} else if updatedID != id {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "folders update response did not match requested folder " + id})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID string `json:"id"`
		}{ID: updatedID})
		return 0
	}
	fmt.Fprintf(stdout, "id\t%s\n", updatedID)
	return 0
}

func runFoldersDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("folders.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm, dryRun := f.LLM(), f.Bool("dry-run")
	id, confirm := f.String("id"), f.String("confirm")
	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required"))
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("deleting a folder is destructive; repeat the folder id with --confirm to proceed"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm value must exactly match --id ("+id+")"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool   `json:"dry_run"`
				ID     string `json:"id"`
			}{DryRun: true, ID: id})
		} else {
			fmt.Fprintf(stdout, "dry_run: would delete folder %s\n", id)
		}
		return 0
	}

	authCtx, cancelAuth := context.WithTimeout(context.Background(), requestTimeout)
	c, sess, authErr := signIn(authCtx, httpClient, baseURL, sessionPath, getenv)
	cancelAuth()
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	params, lockErr := folderLockParams(ctx, c, sess, id)
	if lockErr != nil {
		return exitWithError(stderr, llm, toCLIError(lockErr, "api_error"))
	}
	data, err := c.DoJSON(ctx, http.MethodPut, "/folders", "delete", sess, params, []string{id})
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	if refused := deleteRefusalError(data, "folder "+id); refused != nil {
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

// getFolder fetches a single folder's details. Groupware folders include a
// last_modified timestamp for optimistic locking, while mail folders legitimately
// return null because their backing store does not expose that field.
func getFolder(ctx context.Context, c *client.Client, sess client.Session, id string) (folderDetail, error) {
	params := url.Values{"id": {id}, "tree": {"1"}}
	data, err := c.Do(ctx, http.MethodGet, "/folders", "get", sess, params)
	if err != nil {
		return folderDetail{}, err
	}
	detail, _, err := decodeFolderDetail(data, id)
	return detail, err
}

// folderLockParams reads the folder and returns the query parameters for an update or
// delete. The optional timestamp parameter makes the server refuse the change when the
// folder was modified after it. The API documentation tells clients to send the timestamp
// of the get response envelope; last_modified_utc is the fallback. last_modified is never
// sent, because it is shifted to the user's time zone. When neither value is available
// (mail folders have no modification time), the timestamp is omitted.
func folderLockParams(ctx context.Context, c *client.Client, sess client.Session, id string) (url.Values, error) {
	body, err := c.DoRaw(ctx, http.MethodGet, "/folders", "get", sess, url.Values{"id": {id}, "tree": {"1"}})
	if err != nil {
		return nil, err
	}
	if cliErrors.IsErrorEnvelope(body) {
		var env cliErrors.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode folder error response"}
		}
		return nil, cliErrors.FromEnvelope(env, http.StatusOK)
	}
	var envelope struct {
		Data      json.RawMessage `json:"data"`
		Timestamp *int64          `json:"timestamp"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.Data == nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode folder response"}
	}
	_, utc, err := decodeFolderDetail(envelope.Data, id)
	if err != nil {
		return nil, err
	}
	params := url.Values{}
	switch {
	case envelope.Timestamp != nil && *envelope.Timestamp > 0:
		params.Set("timestamp", strconv.FormatInt(*envelope.Timestamp, 10))
	case utc != nil:
		params.Set("timestamp", strconv.FormatInt(*utc, 10))
	}
	return params, nil
}

// decodeFolderDetail validates a folders get payload for the requested id. It also returns
// last_modified_utc when the server sent a positive value.
func decodeFolderDetail(data []byte, id string) (folderDetail, *int64, error) {
	var f ojFolder
	if err := json.Unmarshal(data, &f); err != nil || strings.TrimSpace(f.ID) == "" || f.ID != id ||
		f.Title == nil || f.Module == nil || f.ParentID == nil || f.Subfolders == nil {
		return folderDetail{}, nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode folder response"}
	}
	if (f.LastModified != nil && *f.LastModified <= 0) || (*f.Module != "mail" && f.LastModified == nil) {
		return folderDetail{}, nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode folder response"}
	}
	var utc *int64
	if f.LastModifiedUTC != nil && *f.LastModifiedUTC > 0 {
		utc = f.LastModifiedUTC
	}
	return folderDetail{
		ID:            f.ID,
		Title:         *f.Title,
		Module:        *f.Module,
		ParentID:      *f.ParentID,
		HasSubfolders: *f.Subfolders,
		LastModified:  f.LastModified,
		Permissions:   f.Permissions,
	}, utc, nil
}

// parsePermissionsFlag parses a --permissions value into a permissions list. A value
// starting with "@" is read as a file path; any other value is parsed as literal JSON,
// following the same convention as contacts' --input flag.
func parsePermissionsFlag(raw string) ([]folderPermission, error) {
	var contents []byte
	if strings.HasPrefix(raw, "@") {
		path := strings.TrimPrefix(raw, "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("could not read --permissions file %s: %w", path, err)
		}
		contents = data
	} else {
		contents = []byte(raw)
	}

	var perms []folderPermission
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&perms); err != nil {
		return nil, fmt.Errorf("--permissions is not a valid JSON array: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("--permissions contains multiple JSON values")
		}
		return nil, fmt.Errorf("--permissions is not a valid JSON array: %w", err)
	}
	if perms == nil {
		return nil, fmt.Errorf("--permissions must be a JSON array, not null")
	}
	return perms, nil
}

func listFolders(ctx context.Context, c *client.Client, sess client.Session, parent string) ([]folderListItem, error) {
	params := url.Values{"parent": {parent}, "columns": {folderListColumns}, "tree": {privateFolderTree}}
	data, err := c.Do(ctx, http.MethodGet, "/folders", "list", sess, params)
	if err != nil {
		return nil, err
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode folders response"}
	}
	folders := make([]folderListItem, 0, len(rows))
	for _, row := range rows {
		item, err := decodeFolderListItem(row)
		if err != nil {
			return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode folders response: " + err.Error()}
		}
		folders = append(folders, item)
	}
	return folders, nil
}

func decodeFolderListItem(row []json.RawMessage) (folderListItem, error) {
	if len(row) != 5 {
		return folderListItem{}, fmt.Errorf("expected 5 columns, got %d", len(row))
	}
	var item folderListItem
	if err := json.Unmarshal(row[0], &item.ID); err != nil {
		return folderListItem{}, err
	}
	if strings.TrimSpace(item.ID) == "" {
		return folderListItem{}, fmt.Errorf("folder row did not include an id")
	}
	var title *string
	if err := json.Unmarshal(row[1], &title); err != nil {
		return folderListItem{}, err
	}
	if title == nil {
		return folderListItem{}, fmt.Errorf("folder row contained a null title")
	}
	item.Title = *title
	var module *string
	if err := json.Unmarshal(row[2], &module); err != nil {
		return folderListItem{}, err
	}
	if module == nil {
		return folderListItem{}, fmt.Errorf("folder row contained a null module")
	}
	item.Module = *module
	var folderType *int
	if err := json.Unmarshal(row[3], &folderType); err != nil {
		return folderListItem{}, err
	}
	if folderType == nil {
		return folderListItem{}, fmt.Errorf("folder row contained a null type")
	}
	item.Type = *folderType
	var hasSubfolders *bool
	if err := json.Unmarshal(row[4], &hasSubfolders); err != nil {
		return folderListItem{}, err
	}
	if hasSubfolders == nil {
		return folderListItem{}, fmt.Errorf("folder row contained a null subfolder flag")
	}
	item.HasSubfolders = *hasSubfolders
	return item, nil
}
