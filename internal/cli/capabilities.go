// ABOUTME: The "capabilities list" command: lists the OX capabilities enabled for the
// ABOUTME: authenticated user via GET /capabilities?action=all.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("capabilities", runCapabilities)
	operations.Register(operations.Operation{
		Name:       "capabilities.list",
		Resource:   "capabilities",
		Action:     "list",
		Summary:    "List OX capabilities enabled for the authenticated user",
		Keywords:   []string{"features"},
		Method:     http.MethodGet,
		Endpoint:   "/capabilities",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Output: operations.OutputSpec{
			Type:   "array",
			Fields: []string{"capabilities"},
		},
	})
}

func runCapabilities(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("capabilities requires an action, e.g. \"capabilities list\""))
	}

	switch args[0] {
	case "list":
		return runCapabilitiesList(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown capabilities action: "+args[0]))
	}
}

func runCapabilitiesList(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("capabilities.list", args)
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

	data, err := c.Do(ctx, http.MethodGet, "/capabilities", "all", sess, nil)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var raw []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil || raw == nil {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode capabilities response"})
	}

	ids := make([]string, 0, len(raw))
	for _, r := range raw {
		if strings.TrimSpace(r.ID) == "" {
			return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "capabilities response contains an item without an id"})
		}
		ids = append(ids, r.ID)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Capabilities []string `json:"capabilities"`
		}{Capabilities: ids})
		return 0
	}

	rows := make([][]string, len(ids))
	for i, id := range ids {
		rows[i] = []string{id}
	}
	render.Table(stdout, []string{"id"}, rows)
	return 0
}
