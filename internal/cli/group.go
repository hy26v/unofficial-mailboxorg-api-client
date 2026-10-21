// ABOUTME: The "group" commands: get/list/create/update/delete groups via the OX
// ABOUTME: middleware's /group module.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
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

func init() {
	registerResource("group", runGroup)
	operations.Register(operations.Operation{
		Name:       "group.get",
		Resource:   "group",
		Action:     "get",
		Summary:    "Get one group by id",
		Method:     http.MethodGet,
		Endpoint:   "/group",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id": {Type: "int", Required: true, Description: "the group's numeric id"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "display_name", "last_modified_utc", "members"},
			ScalarFields: []string{"id", "display_name", "last_modified_utc"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "group.list",
		Resource:   "group",
		Action:     "list",
		Summary:    "List groups matching a search pattern",
		Method:     http.MethodPut,
		Endpoint:   "/group",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"pattern": {Type: "string", Required: false, Description: "search pattern; defaults to \"*\" (all groups)"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "display_name", "last_modified_utc", "members"},
			ScalarFields: []string{"id", "display_name", "last_modified_utc"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "group.create",
		Resource:   "group",
		Action:     "create",
		Summary:    "Create a new group",
		Method:     http.MethodPut,
		Endpoint:   "/group",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"display-name": {Type: "string", Required: true, Description: "human-readable group name"},
			"name":         {Type: "string", Required: true, Description: "simple (technical) group name"},
			"members":      {Type: "string", Required: false, Description: "comma-separated member user ids"},
			"dry-run":      {Type: "bool", Required: false, Description: "validate and show what would be created without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "display_name", "name", "members"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "group.update",
		Resource:   "group",
		Action:     "update",
		Summary:    "Update a group's display name, simple name, or members",
		Method:     http.MethodPut,
		Endpoint:   "/group",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":           {Type: "int", Required: true, Description: "the group's numeric id"},
			"display-name": {Type: "string", Required: false, Description: "new human-readable group name"},
			"name":         {Type: "string", Required: false, Description: "new simple (technical) group name"},
			"members":      {Type: "string", Required: false, Description: "new comma-separated member user ids"},
			"dry-run":      {Type: "bool", Required: false, Description: "validate and show what would change without sending the request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "id", "request"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "group.delete",
		Resource:    "group",
		Action:      "delete",
		Summary:     "Delete a group",
		Method:      http.MethodPut,
		Endpoint:    "/group",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "int", Required: true, Description: "the group's numeric id"},
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

func runGroup(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("group requires an action, e.g. \"group get\""))
	}

	switch args[0] {
	case "get":
		return runGroupGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "list":
		return runGroupList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runGroupCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runGroupUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runGroupDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown group action: "+args[0]))
	}
}

// groupID checks the --id value of a group command: requiredMessage when it is empty,
// otherwise it must be a non-negative integer.
func groupID(idStr, requiredMessage string) (int, *cliErrors.Error) {
	if idStr == "" {
		return 0, invalidArgument(requiredMessage)
	}
	id, err := strconv.Atoi(idStr)
	if err != nil || id < 0 {
		return 0, invalidArgument("--id must be a non-negative integer, got: " + idStr)
	}
	return id, nil
}

// joinMemberIDs formats member ids for human output, e.g. [3, 7] -> "3,7".
func joinMemberIDs(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return strings.Join(parts, ",")
}

func runGroupGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("group.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()

	id, idErr := groupID(f.String("id"), "--id is required, e.g. --id 0")
	if idErr != nil {
		return exitWithError(stderr, llm, idErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	params := url.Values{}
	params.Set("id", strconv.Itoa(id))
	data, err := c.Do(ctx, http.MethodGet, "/group", "get", sess, params)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var group struct {
		ID              *int    `json:"id"`
		DisplayName     *string `json:"display_name"`
		LastModifiedUTC int64   `json:"last_modified_utc"`
		Members         *[]int  `json:"members"`
	}
	if err := json.Unmarshal(data, &group); err != nil || group.ID == nil || *group.ID != id || group.DisplayName == nil ||
		group.LastModifiedUTC <= 0 || group.Members == nil || !validMemberIDs(*group.Members) {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode group response"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID              int    `json:"id"`
			DisplayName     string `json:"display_name"`
			LastModifiedUTC int64  `json:"last_modified_utc"`
			Members         []int  `json:"members"`
		}{ID: *group.ID, DisplayName: *group.DisplayName, LastModifiedUTC: group.LastModifiedUTC, Members: *group.Members})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "id", Value: strconv.Itoa(*group.ID)},
		{Name: "display_name", Value: *group.DisplayName},
		{Name: "last_modified_utc", Value: strconv.FormatInt(group.LastModifiedUTC, 10)},
		{Name: "members", Value: joinMemberIDs(*group.Members)},
	})
	return 0
}

func runGroupList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("group.list", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	pattern := "*"
	if value, given := f.Lookup("pattern"); given {
		pattern = value
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, err := c.DoJSON(ctx, http.MethodPut, "/group", "search", sess, nil, map[string]string{"pattern": pattern})
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var groups []struct {
		ID              *int    `json:"id"`
		DisplayName     *string `json:"display_name"`
		LastModifiedUTC int64   `json:"last_modified_utc"`
		Members         *[]int  `json:"members"`
	}
	if err := json.Unmarshal(data, &groups); err != nil || groups == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode group list response"})
	}
	for _, group := range groups {
		if group.ID == nil || *group.ID < 0 {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "group list response contains an invalid id"})
		}
		if group.DisplayName == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "group list response contains an item without a display name"})
		}
		if group.Members == nil {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "group list response contains an item without members"})
		}
		if !validMemberIDs(*group.Members) {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "group list response contains an invalid member id"})
		}
		if group.LastModifiedUTC <= 0 {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "group list response contains an invalid mutation timestamp"})
		}
	}

	if llm {
		type groupOut struct {
			ID              int    `json:"id"`
			DisplayName     string `json:"display_name"`
			LastModifiedUTC int64  `json:"last_modified_utc"`
			Members         []int  `json:"members"`
		}
		out := make([]groupOut, len(groups))
		for i, g := range groups {
			out[i] = groupOut{ID: *g.ID, DisplayName: *g.DisplayName, LastModifiedUTC: g.LastModifiedUTC, Members: *g.Members}
		}
		render.CompactJSON(stdout, struct {
			Groups []groupOut `json:"groups"`
		}{Groups: out})
		return 0
	}

	rows := make([][]string, len(groups))
	for i, g := range groups {
		rows[i] = []string{strconv.Itoa(*g.ID), *g.DisplayName, strconv.FormatInt(g.LastModifiedUTC, 10), joinMemberIDs(*g.Members)}
	}
	render.Table(stdout, []string{"id", "display_name", "last_modified_utc", "members"}, rows)
	return 0
}

func validMemberIDs(ids []int) bool {
	for _, id := range ids {
		if id <= 0 {
			return false
		}
	}
	return true
}

// parseMemberIDs parses a comma-separated list of member ids into ints, e.g. "3,7" -> [3, 7].
func parseMemberIDs(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	ids := make([]int, 0, len(parts))
	for _, p := range parts {
		member := strings.TrimSpace(p)
		id, err := strconv.Atoi(member)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("member id must be a positive integer: %q", member)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// groupMembers parses the --members value of group create and group update.
func groupMembers(membersStr string) ([]int, *cliErrors.Error) {
	members, err := parseMemberIDs(membersStr)
	if err != nil {
		return nil, invalidArgument("--members must be a comma-separated list of integers, got: " + membersStr)
	}
	return members, nil
}

func runGroupCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("group.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	displayName := f.String("display-name")
	name := f.String("name")

	if displayName == "" {
		return exitWithError(stderr, llm, invalidArgument("--display-name is required"))
	}
	if name == "" {
		return exitWithError(stderr, llm, invalidArgument("--name is required"))
	}
	members, membersErr := groupMembers(f.String("members"))
	if membersErr != nil {
		return exitWithError(stderr, llm, membersErr)
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun      bool   `json:"dry_run"`
				DisplayName string `json:"display_name"`
				Name        string `json:"name"`
				Members     []int  `json:"members"`
			}{DryRun: true, DisplayName: displayName, Name: name, Members: members})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "display_name", Value: displayName},
			{Name: "name", Value: name},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	body := map[string]any{"display_name": displayName, "name": name}
	if members != nil {
		body["members"] = members
	}

	data, err := c.DoJSON(ctx, http.MethodPut, "/group", "new", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var created struct {
		ID *int `json:"id"`
	}
	if err := json.Unmarshal(data, &created); err != nil || created.ID == nil || *created.ID <= 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode group create response"})
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

func runGroupUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("group.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	displayName, displayNameSet := f.Lookup("display-name")
	name, nameSet := f.Lookup("name")
	membersStr, membersSet := f.Lookup("members")

	id, idErr := groupID(f.String("id"), "--id is required")
	if idErr != nil {
		return exitWithError(stderr, llm, idErr)
	}
	if !displayNameSet && !nameSet && !membersSet {
		return exitWithError(stderr, llm, invalidArgument("at least one of --display-name, --name, --members is required"))
	}
	var members []int
	if membersSet {
		var membersErr *cliErrors.Error
		members, membersErr = groupMembers(membersStr)
		if membersErr != nil {
			return exitWithError(stderr, llm, membersErr)
		}
	}

	// The dry run shows the exact body the update sends. --members "" sends
	// "members":null, and the preview must say so.
	body := map[string]any{}
	if displayNameSet {
		body["display_name"] = displayName
	}
	if nameSet {
		body["name"] = name
	}
	if membersSet {
		body["members"] = members
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun  bool           `json:"dry_run"`
				ID      int            `json:"id"`
				Request map[string]any `json:"request"`
			}{DryRun: true, ID: id, Request: body})
			return 0
		}
		requestJSON, _ := json.Marshal(body)
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: strconv.Itoa(id)},
			{Name: "request", Value: string(requestJSON)},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	updateParams := url.Values{}
	updateParams.Set("id", strconv.Itoa(id))
	if _, sendErr := sendGroupChange(ctx, c, sess, id, "update", updateParams, body); sendErr != nil {
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

func runGroupDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("group.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	idStr := f.String("id")
	confirm := f.String("confirm")

	id, idErr := groupID(idStr, "--id is required")
	if idErr != nil {
		return exitWithError(stderr, llm, idErr)
	}
	if confirm == "" {
		return exitWithError(stderr, llm, invalidArgument("--confirm is required and must exactly match --id ("+idStr+") to delete a group"))
	}
	if confirm != idStr {
		return exitWithError(stderr, llm, invalidArgument("--confirm ("+confirm+") does not match --id ("+idStr+"); refusing to delete"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool `json:"dry_run"`
				ID     int  `json:"id"`
			}{DryRun: true, ID: id})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "dry_run", Value: "true"},
			{Name: "id", Value: strconv.Itoa(id)},
		})
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, sendErr := sendGroupChange(ctx, c, sess, id, "delete", url.Values{}, map[string]any{"id": id})
	if sendErr != nil {
		return exitWithError(stderr, llm, sendErr)
	}
	if refused := deleteRefusalError(data, "group "+strconv.Itoa(id)); refused != nil {
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

// sendGroupChange reads the group's current last_modified_utc and then sends action
// ("update" or "delete") with params and body, adding that value as the
// optimistic-locking timestamp, so the caller never has to track it.
func sendGroupChange(ctx context.Context, c *client.Client, sess client.Session, id int, action string, params url.Values, body any) ([]byte, *cliErrors.Error) {
	getParams := url.Values{}
	getParams.Set("id", strconv.Itoa(id))
	getData, err := c.Do(ctx, http.MethodGet, "/group", "get", sess, getParams)
	if err != nil {
		return nil, toCLIError(err, "api_error")
	}
	var current struct {
		ID              *int  `json:"id"`
		LastModifiedUTC int64 `json:"last_modified_utc"`
	}
	if err := json.Unmarshal(getData, &current); err != nil || current.ID == nil || *current.ID != id || current.LastModifiedUTC <= 0 {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode group response"}
	}

	params.Set("timestamp", strconv.FormatInt(current.LastModifiedUTC, 10))
	data, err := c.DoJSON(ctx, http.MethodPut, "/group", action, sess, params, body)
	if err != nil {
		return nil, toCLIError(err, "api_error")
	}
	return data, nil
}
