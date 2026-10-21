// ABOUTME: The "mailfilter" commands: list/create/update/delete mail filter rules via
// ABOUTME: the OX middleware's /mailfilter module (list is GET, new/update/delete are PUT JSON body).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("mailfilter", runMailfilter)
	operations.Register(operations.Operation{
		Name:       "mailfilter.list",
		Resource:   "mailfilter",
		Action:     "list",
		Summary:    "List the authenticated user's mail filter rules",
		Keywords:   []string{"rules", "filters", "forwarding", "vacation", "auto reply", "out of office"},
		Method:     http.MethodGet,
		Endpoint:   "/mailfilter",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"id", "rulename", "active", "flags", "position", "test", "actioncmds"},
			ScalarFields: []string{"id", "rulename", "active", "position"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mailfilter.get",
		Resource:   "mailfilter",
		Action:     "get",
		Summary:    "Get a single mail filter rule by id (the OX mailfilter module has no server-side get action, so this fetches the full rule list and filters locally)",
		Method:     http.MethodGet,
		Endpoint:   "/mailfilter",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"id": {Type: "int", Required: true, Description: "the rule's numeric id"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id", "rulename", "active", "flags", "position", "test", "actioncmds"},
			ScalarFields: []string{"id", "rulename", "active", "position"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mailfilter.create",
		Resource:   "mailfilter",
		Action:     "create",
		Summary:    "Create a new mail filter rule",
		Method:     http.MethodPut,
		Endpoint:   "/mailfilter",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"rule":    {Type: "string", Required: true, Description: "JSON object with rulename, active, test, actioncmds"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be sent, without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"id"},
			ScalarFields: []string{"id"},
			DryRunFields: []string{"dry_run", "rule"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "mailfilter.update",
		Resource:   "mailfilter",
		Action:     "update",
		Summary:    "Update an existing mail filter rule",
		Method:     http.MethodPut,
		Endpoint:   "/mailfilter",
		SideEffect: operations.SideEffectWrite,
		Idempotent: false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "int", Required: true, Description: "the rule's numeric id"},
			"rule":    {Type: "string", Required: true, Description: "JSON object with the fields to update"},
			"dry-run": {Type: "bool", Required: false, Description: "validate and show what would be sent, without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"updated", "id"},
			ScalarFields: []string{"updated", "id"},
			DryRunFields: []string{"dry_run", "rule"},
		},
	})
	operations.Register(operations.Operation{
		Name:        "mailfilter.delete",
		Resource:    "mailfilter",
		Action:      "delete",
		Summary:     "Delete a mail filter rule",
		Method:      http.MethodPut,
		Endpoint:    "/mailfilter",
		SideEffect:  operations.SideEffectDestructive,
		Destructive: true,
		Idempotent:  false,
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "int", Required: true, Description: "the rule's numeric id"},
			"confirm": {Type: "string", Required: true, Description: "must exactly repeat the --id value to confirm deletion"},
			"dry-run": {Type: "bool", Required: false, Description: "validate the confirmation, without sending a request"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"deleted", "id"},
			ScalarFields: []string{"deleted", "id"},
			DryRunFields: []string{"dry_run", "id"},
		},
	})
}

// mailfilterRule is one mail filter rule, decoded generically since the "test" and
// "actioncmds" fields carry the OX mail filter DSL, whose shape varies per rule.
type mailfilterRule struct {
	ID         int             `json:"id"`
	RuleName   string          `json:"rulename"`
	Active     bool            `json:"active"`
	Flags      []string        `json:"flags"`
	Position   int             `json:"position"`
	Test       json.RawMessage `json:"test"`
	ActionCmds json.RawMessage `json:"actioncmds,omitempty"`
}

func decodeMailfilterRules(data []byte) ([]mailfilterRule, error) {
	var rawRules []json.RawMessage
	if err := json.Unmarshal(data, &rawRules); err != nil || rawRules == nil {
		return nil, fmt.Errorf("could not decode mailfilter response")
	}
	rules := make([]mailfilterRule, len(rawRules))
	for i, raw := range rawRules {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			return nil, fmt.Errorf("could not decode mailfilter rule %d", i)
		}
		idRaw, ok := fields["id"]
		if !ok {
			return nil, fmt.Errorf("mailfilter rule %d did not include an id", i)
		}
		var id *int
		if err := json.Unmarshal(idRaw, &id); err != nil || id == nil {
			return nil, fmt.Errorf("mailfilter rule %d did not include a valid id", i)
		}
		nameRaw, ok := fields["rulename"]
		if !ok {
			return nil, fmt.Errorf("mailfilter rule %d did not include a name", i)
		}
		var name *string
		if err := json.Unmarshal(nameRaw, &name); err != nil || name == nil || strings.TrimSpace(*name) == "" {
			return nil, fmt.Errorf("mailfilter rule %d did not include a valid name", i)
		}
		activeRaw, ok := fields["active"]
		if !ok {
			return nil, fmt.Errorf("mailfilter rule %d did not include an active flag", i)
		}
		var active *bool
		if err := json.Unmarshal(activeRaw, &active); err != nil || active == nil {
			return nil, fmt.Errorf("mailfilter rule %d did not include a valid active flag", i)
		}
		actionCmdsRaw, ok := fields["actioncmds"]
		if !ok {
			return nil, fmt.Errorf("mailfilter rule %d did not include action commands", i)
		}
		var actionCmds []json.RawMessage
		if err := json.Unmarshal(actionCmdsRaw, &actionCmds); err != nil || actionCmds == nil {
			return nil, fmt.Errorf("mailfilter rule %d did not include valid action commands", i)
		}
		if err := json.Unmarshal(raw, &rules[i]); err != nil {
			return nil, fmt.Errorf("could not decode mailfilter rule %d", i)
		}
		rules[i].ID = *id
		rules[i].RuleName = *name
		rules[i].Active = *active
	}
	return rules, nil
}

func runMailfilter(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("mailfilter requires an action, e.g. \"mailfilter list\""))
	}

	switch args[0] {
	case "list":
		return runMailfilterList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "get":
		return runMailfilterGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "create":
		return runMailfilterCreate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "update":
		return runMailfilterUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "delete":
		return runMailfilterDelete(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown mailfilter action: "+args[0]))
	}
}

func runMailfilterList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mailfilter.list", args)
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

	data, err := c.Do(ctx, http.MethodGet, "/mailfilter", "list", sess, nil)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	rules, decodeErr := decodeMailfilterRules(data)
	if decodeErr != nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mailfilter response"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Rules []mailfilterRule `json:"rules"`
		}{Rules: rules})
		return 0
	}

	rows := make([][]string, len(rules))
	for i, r := range rules {
		rows[i] = []string{strconv.Itoa(r.ID), r.RuleName, strconv.FormatBool(r.Active)}
	}
	render.Table(stdout, []string{"id", "rulename", "active"}, rows)
	return 0
}

// runMailfilterGet fetches a single mail filter rule by id. The OX mailfilter module
// on this deployment has no server-side "get" action (it returns a generic SVL-0001
// server error for it), so this calls "list" and filters the results locally.
func runMailfilterGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mailfilter.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	idStr := f.String("id")

	if idStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 7"))
	}
	id, convErr := strconv.Atoi(idStr)
	if convErr != nil || id < 0 {
		return exitWithError(stderr, llm, invalidArgument("--id must be a non-negative integer, got: "+idStr))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, err := c.Do(ctx, http.MethodGet, "/mailfilter", "list", sess, nil)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	rules, decodeErr := decodeMailfilterRules(data)
	if decodeErr != nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mailfilter response"})
	}

	for _, r := range rules {
		if r.ID != id {
			continue
		}
		if llm {
			render.CompactJSON(stdout, r)
			return 0
		}
		fmt.Fprintf(stdout, "id: %d\nrulename: %s\nactive: %t\n", r.ID, render.SafeText(r.RuleName), r.Active)
		return 0
	}

	return exitWithError(stderr, llm, &cliErrors.Error{Code: "not_found", Message: "mail filter rule not found: id=" + idStr})
}

// parseMailfilterRuleBody decodes --rule's JSON value into a map, so unrecognized/extra
// fields the caller supplies are still forwarded to the API unchanged.
func parseMailfilterRuleBody(raw string) (map[string]any, error) {
	var body map[string]any
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return nil, fmt.Errorf("--rule must be valid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("--rule must be valid JSON: multiple JSON values")
		}
		return nil, fmt.Errorf("--rule must be valid JSON: %w", err)
	}
	if body == nil {
		return nil, fmt.Errorf("--rule must be a JSON object, not null")
	}
	if actionCmds, present := body["actioncmds"]; present {
		if _, ok := actionCmds.([]any); !ok {
			return nil, fmt.Errorf(`--rule field "actioncmds" must be a JSON array`)
		}
	}
	return body, nil
}

func runMailfilterCreate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mailfilter.create", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	ruleRaw := f.String("rule")

	if ruleRaw == "" {
		return exitWithError(stderr, llm, invalidArgument("--rule is required, e.g. --rule '{\"rulename\":\"...\",\"actioncmds\":[...]}'"))
	}

	body, perr := parseMailfilterRuleBody(ruleRaw)
	if perr != nil {
		return exitWithError(stderr, llm, invalidArgument(perr.Error()))
	}
	rulename, _ := body["rulename"].(string)
	if rulename == "" {
		return exitWithError(stderr, llm, invalidArgument("--rule must include a non-empty \"rulename\" field"))
	}
	if _, ok := body["actioncmds"]; !ok {
		return exitWithError(stderr, llm, invalidArgument("--rule must include an \"actioncmds\" field"))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool           `json:"dry_run"`
				Rule   map[string]any `json:"rule"`
			}{DryRun: true, Rule: body})
			return 0
		}
		fmt.Fprintf(stdout, "dry run: would create mail filter rule %q\n", rulename)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, err := c.DoJSON(ctx, http.MethodPut, "/mailfilter", "new", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	// The rule id is the caller's only handle on the rule that was just created, so a
	// response it cannot be read from is an error rather than a reported id of 0. A pointer
	// distinguishes a real numeric zero from JSON null or a missing value.
	var id *int
	if len(data) == 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "mailfilter create response carried no rule id"})
	}
	if err := json.Unmarshal(data, &id); err != nil || id == nil || *id < 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode mailfilter create response"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			ID int `json:"id"`
		}{ID: *id})
		return 0
	}
	fmt.Fprintf(stdout, "created mail filter rule %d\n", *id)
	return 0
}

func runMailfilterUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mailfilter.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	idStr, ruleRaw := f.String("id"), f.String("rule")

	if idStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 7"))
	}
	id, convErr := strconv.Atoi(idStr)
	if convErr != nil || id < 0 {
		return exitWithError(stderr, llm, invalidArgument("--id must be a non-negative integer, got: "+idStr))
	}
	if ruleRaw == "" {
		return exitWithError(stderr, llm, invalidArgument("--rule is required, e.g. --rule '{\"active\":false}'"))
	}

	body, perr := parseMailfilterRuleBody(ruleRaw)
	if perr != nil {
		return exitWithError(stderr, llm, invalidArgument(perr.Error()))
	}
	body["id"] = id

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool           `json:"dry_run"`
				Rule   map[string]any `json:"rule"`
			}{DryRun: true, Rule: body})
			return 0
		}
		fmt.Fprintf(stdout, "dry run: would update mail filter rule %d\n", id)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	_, err := c.DoJSON(ctx, http.MethodPut, "/mailfilter", "update", sess, nil, body)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Updated bool `json:"updated"`
			ID      int  `json:"id"`
		}{Updated: true, ID: id})
		return 0
	}
	fmt.Fprintf(stdout, "updated mail filter rule %d\n", id)
	return 0
}

func runMailfilterDelete(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("mailfilter.delete", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	idStr, confirm := f.String("id"), f.String("confirm")

	if idStr == "" {
		return exitWithError(stderr, llm, invalidArgument("--id is required, e.g. --id 7"))
	}
	id, convErr := strconv.Atoi(idStr)
	if convErr != nil || id < 0 {
		return exitWithError(stderr, llm, invalidArgument("--id must be a non-negative integer, got: "+idStr))
	}
	if confirm != idStr {
		return exitWithError(stderr, llm, invalidArgument(fmt.Sprintf("refusing to delete: --confirm must exactly repeat --id (%q), got %q", idStr, confirm)))
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool `json:"dry_run"`
				ID     int  `json:"id"`
			}{DryRun: true, ID: id})
			return 0
		}
		fmt.Fprintf(stdout, "dry run: would delete mail filter rule %d\n", id)
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	_, err := c.DoJSON(ctx, http.MethodPut, "/mailfilter", "delete", sess, nil, map[string]any{"id": id})
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Deleted bool `json:"deleted"`
			ID      int  `json:"id"`
		}{Deleted: true, ID: id})
		return 0
	}
	fmt.Fprintf(stdout, "deleted mail filter rule %d\n", id)
	return 0
}
