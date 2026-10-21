// ABOUTME: The "quota get" command: fetches filestore quota usage for the authenticated
// ABOUTME: user via GET /quota?action=filestore.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("quota", runQuota)
	operations.Register(operations.Operation{
		Name:       "quota.get",
		Resource:   "quota",
		Action:     "get",
		Summary:    "Show filestore quota usage",
		Keywords:   []string{"storage", "space", "usage"},
		Method:     http.MethodGet,
		Endpoint:   "/quota",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"type": {Type: "string", Required: true, Description: "quota type; only \"filestore\" is supported"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"quota", "use"},
			ScalarFields: []string{"quota", "use"},
		},
	})
}

func runQuota(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("quota requires an action, e.g. \"quota get\""))
	}

	switch args[0] {
	case "get":
		return runQuotaGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown quota action: "+args[0]))
	}
}

func runQuotaGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("quota.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	quotaType := f.String("type")

	if quotaType == "" {
		return exitWithError(stderr, llm, invalidArgument("--type is required, e.g. --type filestore"))
	}
	if quotaType != "filestore" {
		return exitWithError(stderr, llm, invalidArgument("unsupported --type: "+quotaType+" (only \"filestore\" is supported)"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	data, err := c.Do(ctx, http.MethodGet, "/quota", "filestore", sess, nil)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var quota struct {
		Quota *int64 `json:"quota"`
		Use   *int64 `json:"use"`
	}
	if err := json.Unmarshal(data, &quota); err != nil || quota.Quota == nil || quota.Use == nil ||
		*quota.Quota < 0 || *quota.Use < 0 {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode quota response"})
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Quota int64 `json:"quota"`
			Use   int64 `json:"use"`
		}{Quota: *quota.Quota, Use: *quota.Use})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "quota", Value: strconv.FormatInt(*quota.Quota, 10)},
		{Name: "use", Value: strconv.FormatInt(*quota.Use, 10)},
	})
	return 0
}
