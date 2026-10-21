// ABOUTME: The "snippet" resource commands (signatures): list, get, create, update, and
// ABOUTME: delete via GET/PUT /snippet.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	snippetDefaultType   = "signature"
	snippetDefaultModule = "io.ox/mail"
)

func init() {
	registerResource("snippet", runSnippet)
	operations.Register(operations.Operation{
		Name:       "snippet.list",
		Resource:   "snippet",
		Action:     "list",
		Summary:    "List all snippets (signatures)",
		Keywords:   []string{"signature"},
		Method:     http.MethodGet,
		Endpoint:   "/snippet",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "displayname", "content", "module", "type", "shared", "createdby"},
			ScalarFields: []string{"id", "displayname", "content", "module", "type", "shared", "createdby"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "snippet.get",
		Resource:   "snippet",
		Action:     "get",
		Summary:    "Get one snippet by id",
		Method:     http.MethodGet,
		Endpoint:   "/snippet",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id": {Type: "string", Required: true, Description: "the snippet's id"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "displayname", "content", "module", "type", "shared", "createdby"},
			ScalarFields: []string{"id", "displayname", "content", "module", "type", "shared", "createdby"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "snippet.create",
		Resource:   "snippet",
		Action:     "create",
		Summary:    "Create a new snippet (signature)",
		Method:     http.MethodPut,
		Endpoint:   "/snippet",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"displayname": {Type: "string", Required: true, Description: "display name for the snippet"},
			"content":     {Type: "string", Required: true, Description: "the snippet's body content"},
			"module":      {Type: "string", Required: false, Description: "owning module; defaults to \"io.ox/mail\""},
			"type":        {Type: "string", Required: false, Description: "snippet type; defaults to \"signature\""},
			"dry-run":     {Type: "bool", Required: false, Description: "validate and show what would be created, without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "operation", "would_send"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "snippet.update",
		Resource:   "snippet",
		Action:     "update",
		Summary:    "Update an existing snippet's content or display name",
		Method:     http.MethodPut,
		Endpoint:   "/snippet",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id":          {Type: "string", Required: true, Description: "the snippet's id"},
			"content":     {Type: "string", Required: false, Description: "new body content"},
			"displayname": {Type: "string", Required: false, Description: "new display name"},
			"dry-run":     {Type: "bool", Required: false, Description: "validate and show what would be updated, without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "displayname", "content", "module", "type", "shared", "createdby"},
			ScalarFields: []string{"id", "displayname", "content", "module", "type", "shared", "createdby"},
			DryRunFields: []string{"dry_run", "operation", "would_send"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "snippet.delete",
		Resource:    "snippet",
		Action:      "delete",
		Summary:     "Delete a snippet permanently",
		Method:      http.MethodGet,
		Endpoint:    "/snippet",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  true,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "string", Required: true, Description: "the snippet's id"},
			"confirm": {Type: "string", Required: true, Description: "must exactly repeat --id to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be deleted, without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "deleted"},
			ScalarFields: []string{"id", "deleted"},
			DryRunFields: []string{"dry_run", "operation", "would_send"},
		},
	})
}

type snippetOut struct {
	ID          string  `json:"id"`
	DisplayName *string `json:"displayname"`
	Content     *string `json:"content"`
	Module      *string `json:"module"`
	Type        *string `json:"type"`
	Shared      *bool   `json:"shared"`
	CreatedBy   *int    `json:"createdby"`
}

func runSnippet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("snippet requires an action, e.g. \"snippet list\""))
	}

	switch args[0] {
	case "list":
		return runSnippetList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runSnippetGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runSnippetCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runSnippetUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runSnippetDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown snippet action: "+args[0]))
	}
}

func runSnippetList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("snippet.list", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, err := c.Do(ctx, http.MethodGet, "/snippet", "all", sess, nil)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var snippets []snippetOut
	if err := json.Unmarshal(data, &snippets); err != nil || snippets == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode snippet list response"})
	}
	for _, snippet := range snippets {
		if strings.TrimSpace(snippet.ID) == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet list response contains an item without an id"})
		}
		if snippet.DisplayName == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet list response contains an item without a display name"})
		}
		if snippet.Content == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet list response contains an item without content"})
		}
		if snippet.Module == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet list response contains an item without a module"})
		}
		if snippet.Type == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet list response contains an item without a type"})
		}
		if snippet.Shared == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet list response contains an item without a shared flag"})
		}
		if snippet.CreatedBy == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet list response contains an item without a creator"})
		}
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Snippets []snippetOut `json:"snippets"`
		}{Snippets: snippets})
		return 0
	}

	rows := make([][]string, len(snippets))
	for i, s := range snippets {
		rows[i] = []string{s.ID, snippetString(s.DisplayName), snippetString(s.Type), snippetString(s.Module)}
	}
	render.Table(stdout, []string{"id", "displayname", "type", "module"}, rows)
	return 0
}

func runSnippetGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("snippet.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	id := f.String("id")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 2"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	params := url.Values{}
	params.Set("id", id)
	data, err := c.Do(ctx, http.MethodGet, "/snippet", "get", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var s snippetOut
	if err := json.Unmarshal(data, &s); err != nil || strings.TrimSpace(s.ID) == "" || s.ID != id ||
		s.DisplayName == nil || s.Content == nil || s.Module == nil || s.Type == nil || s.Shared == nil ||
		s.CreatedBy == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode snippet response"})
	}

	if llm {
		render.CompactJSON(stdout, s)
		return 0
	}

	render.KeyValueTable(stdout, snippetFields(s))
	return 0
}

func snippetFields(s snippetOut) []render.Field {
	shared := "false"
	if s.Shared != nil && *s.Shared {
		shared = "true"
	}
	return []render.Field{
		{Name: "id", Value: s.ID},
		{Name: "displayname", Value: snippetString(s.DisplayName)},
		{Name: "content", Value: snippetString(s.Content)},
		{Name: "module", Value: snippetString(s.Module)},
		{Name: "type", Value: snippetString(s.Type)},
		{Name: "shared", Value: shared},
	}
}

func snippetString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func runSnippetCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("snippet.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	displayName := f.String("displayname")
	content := f.String("content")
	module := snippetDefaultModule
	if value, given := f.Lookup("module"); given {
		module = value
	}
	snippetType := snippetDefaultType
	if value, given := f.Lookup("type"); given {
		snippetType = value
	}

	if displayName == "" {
		return exitWithError(stderr, llm, invalidArgument("--displayname is required"))
	}
	if content == "" {
		return exitWithError(stderr, llm, invalidArgument("--content is required"))
	}

	body := map[string]string{
		"displayname": displayName,
		"content":     content,
		"module":      module,
		"type":        snippetType,
	}

	if dryRun {
		renderDryRun(stdout, llm, "snippet.create", body)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, err := c.DoJSON(ctx, http.MethodPut, "/snippet", "new", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var id string
	if err := json.Unmarshal(data, &id); err != nil || strings.TrimSpace(id) == "" {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode snippet create response"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID string `json:"id"`
		}{ID: id})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: id}})
	return 0
}

func runSnippetUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("snippet.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	content, hasContent := f.Lookup("content")
	displayName, hasDisplayName := f.Lookup("displayname")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 2"))
	}
	if !hasContent && !hasDisplayName {
		return exitWithError(stderr, llm, invalidArgument("at least one of --content or --displayname is required"))
	}

	body := map[string]string{}
	if hasContent {
		body["content"] = content
	}
	if hasDisplayName {
		body["displayname"] = displayName
	}

	if dryRun {
		would := map[string]string{"id": id}
		for name, value := range body {
			would[name] = value
		}
		renderDryRun(stdout, llm, "snippet.update", would)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	params := url.Values{}
	params.Set("id", id)
	data, err := c.DoJSON(ctx, http.MethodPut, "/snippet", "update", sess, params, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var s snippetOut
	if err := json.Unmarshal(data, &s); err != nil || strings.TrimSpace(s.ID) == "" || s.ID != id ||
		s.DisplayName == nil || s.Content == nil || s.Module == nil || s.Type == nil || s.Shared == nil ||
		s.CreatedBy == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode snippet update response"})
	}
	if (hasContent && *s.Content != content) || (hasDisplayName && *s.DisplayName != displayName) {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "snippet update response did not contain the requested fields"})
	}

	if llm {
		render.CompactJSON(stdout, s)
		return 0
	}

	render.KeyValueTable(stdout, snippetFields(s))
	return 0
}

func runSnippetDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("snippet.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	id := f.String("id")
	confirm := f.String("confirm")

	if id == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 2"))
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("--confirm is required and must repeat --id exactly to delete a snippet"))
	}
	if confirm != id {
		return exitWithError(stderr, llm, invalidArgument("--confirm value does not match --id; refusing to delete"))
	}

	if dryRun {
		renderDryRun(stdout, llm, "snippet.delete", map[string]string{"id": id})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	params := url.Values{}
	params.Set("id", id)
	_, err := c.Do(ctx, http.MethodGet, "/snippet", "delete", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID      string `json:"id"`
			Deleted bool   `json:"deleted"`
		}{ID: id, Deleted: true})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{{Name: "id", Value: id}, {Name: "deleted", Value: "true"}})
	return 0
}

// renderDryRun writes what a mutating snippet operation would do, without sending any
// request. It is shared by create/update/delete's --dry-run handling.
func renderDryRun(stdout io.Writer, llm bool, operation string, fields map[string]string) {
	if llm {
		render.CompactJSON(stdout, struct {
			DryRun    bool              `json:"dry_run"`
			Operation string            `json:"operation"`
			Would     map[string]string `json:"would_send"`
		}{DryRun: true, Operation: operation, Would: fields})
		return
	}

	// Sorted, because ranging a map would print the same command's fields in a different
	// order on every run. The LLM branch above is already stable: encoding/json sorts map keys.
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)

	rows := []render.Field{{Name: "dry_run", Value: "true"}, {Name: "operation", Value: operation}}
	for _, name := range names {
		rows = append(rows, render.Field{Name: name, Value: fields[name]})
	}
	render.KeyValueTable(stdout, rows)
}
