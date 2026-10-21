// ABOUTME: The "whoami" command (GET /user?action=get) and the "user update" command
// ABOUTME: (PUT /user?action=update), both registering themselves as top-level resources.
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
	"time"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("whoami", runWhoami)
	operations.Register(operations.Operation{
		Name:       "whoami",
		Resource:   "whoami",
		Action:     "get",
		Summary:    "Show the authenticated user",
		Keywords:   []string{"me", "my account", "profile", "user", "identity", "email address"},
		Method:     http.MethodGet,
		Endpoint:   "/user",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"email", "display_name", "user_id"},
			ScalarFields: []string{"email", "display_name", "user_id"},
		},
	})

	registerResource("user", runUser)
	operations.Register(operations.Operation{
		Name:       "user.update",
		Resource:   "user",
		Action:     "update",
		Summary:    "Update the authenticated user's display name and/or timezone",
		Method:     http.MethodPut,
		Endpoint:   "/user",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"display-name": {Type: "string", Required: false, Description: "new display name (at least one of --display-name/--timezone required)"},
			"timezone":     {Type: "string", Required: false, Description: "new IANA timezone, e.g. Europe/Berlin"},
			"dry-run":      {Type: "boolean", Required: false, Description: "validate and show what would change, without sending the update"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"user_id", "display_name", "timezone"},
			ScalarFields: []string{"user_id", "display_name", "timezone"},
			DryRunFields: []string{"dry_run", "before", "after"},
		},
	})
}

func runWhoami(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("whoami", args)
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

	data, err := c.Do(ctx, http.MethodGet, "/user", "get", sess, nil)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var user struct {
		Email       string `json:"email1"`
		DisplayName string `json:"display_name"`
		UserID      *int   `json:"user_id"`
	}
	if err := json.Unmarshal(data, &user); err != nil || strings.TrimSpace(user.Email) == "" || user.UserID == nil || *user.UserID <= 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode user response"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Email       string `json:"email"`
			DisplayName string `json:"display_name"`
			UserID      int    `json:"user_id"`
		}{Email: user.Email, DisplayName: user.DisplayName, UserID: *user.UserID})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "email", Value: user.Email},
		{Name: "display_name", Value: user.DisplayName},
	})
	return 0
}

func runUser(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("user requires an action, e.g. \"user update\""))
	}

	switch args[0] {
	case "update":
		return runUserUpdate(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown user action: "+args[0]))
	}
}

// userProfile is the subset of the /user?action=get response runUserUpdate needs: the
// numeric id and current field values required to build an update request and preview.
type userProfile struct {
	ID          *int   `json:"id"`
	DisplayName string `json:"display_name"`
	Timezone    string `json:"timezone"`
}

func runUserUpdate(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("user.update", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	var displayName, timezone *string
	if value, given := f.Lookup("display-name"); given {
		displayName = &value
	}
	if value, given := f.Lookup("timezone"); given {
		timezone = &value
	}

	if displayName == nil && timezone == nil {
		return exitWithError(stderr, llm, invalidArgument("at least one of --display-name or --timezone is required"))
	}
	if timezone != nil {
		if _, err := time.LoadLocation(*timezone); err != nil {
			return exitWithError(stderr, llm, invalidArgument(fmt.Sprintf("invalid --timezone %q: %v", *timezone, err)))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	// Read the current profile first: the numeric id and timestamp are required by the
	// update request (id to address the record, timestamp for optimistic locking so a
	// concurrent update elsewhere is not silently lost), and the current field values let
	// dry-run show a real before/after preview.
	getData, timestamp, err := c.DoWithTimestamp(ctx, http.MethodGet, "/user", "get", sess, nil)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var current userProfile
	if err := json.Unmarshal(getData, &current); err != nil || current.ID == nil || *current.ID <= 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode user response"})
	}

	newDisplayName := current.DisplayName
	if displayName != nil {
		newDisplayName = *displayName
	}
	newTimezone := current.Timezone
	if timezone != nil {
		newTimezone = *timezone
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun bool `json:"dry_run"`
				Before struct {
					DisplayName string `json:"display_name"`
					Timezone    string `json:"timezone"`
				} `json:"before"`
				After struct {
					DisplayName string `json:"display_name"`
					Timezone    string `json:"timezone"`
				} `json:"after"`
			}{
				DryRun: true,
				Before: struct {
					DisplayName string `json:"display_name"`
					Timezone    string `json:"timezone"`
				}{DisplayName: current.DisplayName, Timezone: current.Timezone},
				After: struct {
					DisplayName string `json:"display_name"`
					Timezone    string `json:"timezone"`
				}{DisplayName: newDisplayName, Timezone: newTimezone},
			})
			return 0
		}
		fmt.Fprintf(stdout, "dry-run: no change sent (the current profile was read)\ndisplay_name: %q -> %q\ntimezone: %q -> %q\n",
			current.DisplayName, newDisplayName, current.Timezone, newTimezone)
		return 0
	}

	body := map[string]any{}
	if displayName != nil {
		body["display_name"] = *displayName
	}
	if timezone != nil {
		body["timezone"] = *timezone
	}

	params := url.Values{
		"id":        {strconv.Itoa(*current.ID)},
		"timestamp": {strconv.FormatInt(timestamp, 10)},
	}
	if _, err := c.DoJSON(ctx, http.MethodPut, "/user", "update", sess, params, body); err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if llm {
		render.CompactJSON(stdout, struct {
			UserID      int    `json:"user_id"`
			DisplayName string `json:"display_name"`
			Timezone    string `json:"timezone"`
		}{UserID: *current.ID, DisplayName: newDisplayName, Timezone: newTimezone})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "display_name", Value: newDisplayName},
		{Name: "timezone", Value: newTimezone},
	})
	return 0
}
