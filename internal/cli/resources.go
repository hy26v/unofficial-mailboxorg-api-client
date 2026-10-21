// ABOUTME: The "resources" commands: search/create/update/delete OX resources
// ABOUTME: (rooms/equipment) via the OX middleware's /resource module.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const resourcesDefaultPattern = "*"

// resourceTimestampFallback is used as the optimistic-locking timestamp for resource
// update/delete requests when the resource's last_modified_utc could not be parsed as
// an epoch-millis integer. The live mailbox.org deployment returns last_modified_utc as
// a Java Date.toString() string (e.g. "Fri Jul 24 03:52:19 CEST 2026") for the resource
// module's get action, rather than the epoch-millis integer other modules return; an
// arbitrarily large timestamp satisfies the server's optimistic-lock check regardless.
const resourceTimestampFallback = "9999999999999"

// parseResourceTimestamp parses a last_modified_utc JSON value as an epoch-millis integer,
// falling back to resourceTimestampFallback if it is not a JSON number (e.g. a date string).
func parseResourceTimestamp(raw json.RawMessage) string {
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		return strconv.FormatInt(ms, 10)
	}
	return resourceTimestampFallback
}

func validResourceTimestamp(raw json.RawMessage) bool {
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		return ms > 0
	}
	var date string
	return json.Unmarshal(raw, &date) == nil && strings.TrimSpace(date) != ""
}

func init() {
	registerResource("resources", runResources)
	operations.Register(operations.Operation{
		Name:       "resources.search",
		Resource:   "resources",
		Action:     "search",
		Summary:    "Search OX resources (rooms/equipment) by name pattern",
		Method:     http.MethodPut,
		Endpoint:   "/resource",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"pattern": {Type: "string", Required: false, Description: "search pattern; \"*\" matches all resources (default \"*\")"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "display_name", "email"},
			ScalarFields: []string{"id", "display_name", "email"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "resources.create",
		Resource:   "resources",
		Action:     "create",
		Summary:    "Create a new OX resource (room/equipment)",
		Method:     http.MethodPut,
		Endpoint:   "/resource",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"display-name": {Type: "string", Required: true, Description: "human-readable resource name"},
			"name":         {Type: "string", Required: true, Description: "simple (technical) resource name"},
			"mailaddress":  {Type: "string", Required: true, Description: "the resource's booking email address"},
			"description":  {Type: "string", Required: false, Description: "free-text description"},
			"dry-run":      {Type: "bool", Required: false, Description: "validate and show what would be created without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "display_name", "name", "mailaddress"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "resources.update",
		Resource:   "resources",
		Action:     "update",
		Summary:    "Update an OX resource's display name, simple name, mail address, or description",
		Method:     http.MethodPut,
		Endpoint:   "/resource",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":           {Type: "int", Required: true, Description: "the resource's numeric id"},
			"display-name": {Type: "string", Required: false, Description: "new human-readable resource name"},
			"name":         {Type: "string", Required: false, Description: "new simple (technical) resource name"},
			"mailaddress":  {Type: "string", Required: false, Description: "new booking email address"},
			"description":  {Type: "string", Required: false, Description: "new free-text description"},
			"dry-run":      {Type: "bool", Required: false, Description: "validate and show what would change without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "resources.delete",
		Resource:    "resources",
		Action:      "delete",
		Summary:     "Delete an OX resource",
		Method:      http.MethodPut,
		Endpoint:    "/resource",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "int", Required: true, Description: "the resource's numeric id"},
			"confirm": {Type: "string", Required: true, Description: "must exactly match --id to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be deleted without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "id"},
			ScalarFields: []string{"deleted", "id"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
}

func runResources(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("resources requires an action, e.g. \"resources search\""))
	}

	switch args[0] {
	case "search":
		return runResourcesSearch(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runResourcesCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runResourcesUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runResourcesDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown resources action: "+args[0]))
	}
}

type resource struct {
	ID          int    `json:"id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
}

func runResourcesSearch(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("resources.search", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	pattern := resourcesDefaultPattern
	if value, given := f.Lookup("pattern"); given {
		pattern = value
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, err := c.DoJSON(ctx, http.MethodPut, "/resource", "search", sess, nil, map[string]string{"pattern": pattern})
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var raw []struct {
		ID          int     `json:"id"`
		DisplayName *string `json:"display_name"`
		MailAddress *string `json:"mailaddress"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode resources response"})
	}

	resources := make([]resource, 0, len(raw))
	for _, r := range raw {
		if r.ID <= 0 {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "resources response contains an item without a valid id"})
		}
		if r.DisplayName == nil || strings.TrimSpace(*r.DisplayName) == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "resources response contains an item without a display name"})
		}
		if r.MailAddress == nil || strings.TrimSpace(*r.MailAddress) == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "resources response contains an item without a mail address"})
		}
		resources = append(resources, resource{ID: r.ID, DisplayName: *r.DisplayName, Email: *r.MailAddress})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Resources []resource `json:"resources"`
		}{Resources: resources})
		return 0
	}

	rows := make([][]string, len(resources))
	for i, r := range resources {
		rows[i] = []string{strconv.Itoa(r.ID), r.DisplayName, r.Email}
	}
	render.Table(stdout, []string{"id", "display_name", "email"}, rows)
	return 0
}

func runResourcesCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("resources.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	displayName := f.String("display-name")
	name := f.String("name")
	mailaddress := f.String("mailaddress")
	description := f.String("description")

	if displayName == "" {
		return exitWithError(stderr, llm, invalidArgument("--display-name is required"))
	}
	if name == "" {
		return exitWithError(stderr, llm, invalidArgument("--name is required"))
	}
	if mailaddress == "" {
		return exitWithError(stderr, llm, invalidArgument("--mailaddress is required"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun      bool   `json:"dry_run"`
				DisplayName string `json:"display_name"`
				Name        string `json:"name"`
				MailAddress string `json:"mailaddress"`
			}{DryRun: true, DisplayName: displayName, Name: name, MailAddress: mailaddress})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "display_name", Value: displayName},
			{Name: "name", Value: name},
			{Name: "mailaddress", Value: mailaddress},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	body := map[string]any{"display_name": displayName, "name": name, "mailaddress": mailaddress}
	if description != "" {
		body["description"] = description
	}

	data, err := c.DoJSON(ctx, http.MethodPut, "/resource", "new", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var created struct {
		ID *int `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == nil || *created.ID <= 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode resource create response"})
	}
	id := *created.ID

	if llm {
		render.CompactJSON(stdout, struct {
			ID int `json:"id"`
		}{ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: strconv.Itoa(id)}})
	return 0
}

// resourceID checks the --id value of resources update and delete: it is required and
// must be a positive integer.
func resourceID(idStr string) (int, *cliErrors.Error) {
	if idStr == "" {
		return 0, invalidArgument("--id is required")
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id <= 0 {
		return 0, invalidArgument("--id must be a positive integer, got: " + idStr)
	}
	return id, nil
}

// renderResourceIDDryRun writes the dry-run result of resources update and delete.
func renderResourceIDDryRun(stdout io.Writer, llm bool, id int) {
	if llm {
		render.CompactJSON(stdout, struct {
			DryRun bool `json:"dry_run"`
			ID     int  `json:"id"`
		}{DryRun: true, ID: id})
		return
	}
	render.KeyValueTable(stdout, []render.Field{
		{Name: "dry_run", Value: "true"},
		{Name: "id", Value: strconv.Itoa(id)},
	})
}

func runResourcesUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("resources.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	displayName, displayNameSet := f.Lookup("display-name")
	name, nameSet := f.Lookup("name")
	mailaddress, mailaddressSet := f.Lookup("mailaddress")
	description, descriptionSet := f.Lookup("description")

	id, idErr := resourceID(f.String("id"))
	if idErr != nil {
		return exitWithError(stderr, llm, idErr)
	}
	if !displayNameSet && !nameSet && !mailaddressSet && !descriptionSet {
		return exitWithError(stderr, llm, invalidArgument("at least one of --display-name, --name, --mailaddress, --description is required"))
	}

	if dryRun {
		renderResourceIDDryRun(stdout, llm, id)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	body := map[string]any{}
	if displayNameSet {
		body["display_name"] = displayName
	}
	if nameSet {
		body["name"] = name
	}
	if mailaddressSet {
		body["mailaddress"] = mailaddress
	}
	if descriptionSet {
		body["description"] = description
	}

	updateParams := url.Values{}
	updateParams.Set("id", strconv.Itoa(id))
	if _, sendErr := sendResourceChange(ctx, c, sess, id, "update", updateParams, body); sendErr != nil {
		return exitWithError(stderr, llm, sendErr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID int `json:"id"`
		}{ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: strconv.Itoa(id)}})
	return 0
}

func runResourcesDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("resources.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	idStr := f.String("id")
	confirm := f.String("confirm")

	id, idErr := resourceID(idStr)
	if idErr != nil {
		return exitWithError(stderr, llm, idErr)
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("--confirm is required and must exactly match --id ("+idStr+") to delete a resource"))
	}
	if confirm != idStr {
		return exitWithError(stderr, llm, invalidArgument("--confirm ("+confirm+") does not match --id ("+idStr+"); refusing to delete"))
	}

	if dryRun {
		renderResourceIDDryRun(stdout, llm, id)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, sendErr := sendResourceChange(ctx, c, sess, id, "delete", url.Values{}, map[string]any{"id": id})
	if sendErr != nil {
		return exitWithError(stderr, llm, sendErr)
	}
	if refused := deleteRefusalError(data, "resource "+strconv.Itoa(id)); refused != nil {
		return exitWithError(stderr, llm, refused)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Deleted bool `json:"deleted"`
			ID      int  `json:"id"`
		}{Deleted: true, ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "deleted", Value: "true"},
		{Name: "id", Value: strconv.Itoa(id)},
	})
	return 0
}

// sendResourceChange reads the resource's current last_modified_utc and then sends
// action ("update" or "delete") with params and body, adding that value as the
// optimistic-locking timestamp, so the caller never has to track it.
func sendResourceChange(ctx context.Context, c *client.Client, sess client.Session, id int, action string, params url.Values, body any) ([]byte, *cliErrors.Error) {
	getParams := url.Values{}
	getParams.Set("id", strconv.Itoa(id))
	getData, err := c.Do(ctx, http.MethodGet, "/resource", "get", sess, getParams)
	if err != nil {
		return nil, toCLIError(err, "api_error")
	}
	var current struct {
		ID              *int            `json:"id"`
		LastModifiedUTC json.RawMessage `json:"last_modified_utc"`
	}
	if err := json.Unmarshal(getData, &current); err != nil || current.ID == nil || *current.ID != id ||
		!validResourceTimestamp(current.LastModifiedUTC) {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode resource response"}
	}

	params.Set("timestamp", parseResourceTimestamp(current.LastModifiedUTC))
	data, err := c.DoJSON(ctx, http.MethodPut, "/resource", action, sess, params, body)
	if err != nil {
		return nil, toCLIError(err, "api_error")
	}
	return data, nil
}
