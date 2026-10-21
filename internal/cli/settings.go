// ABOUTME: The "settings get"/"settings set" commands: read and write jslob tree settings
// ABOUTME: (module jslob) via PUT /jslob?action=list and a merging PUT /jslob?action=update.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/client"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func init() {
	registerResource("settings", runSettings)
	operations.Register(operations.Operation{
		Name:       "settings.get",
		Resource:   "settings",
		Action:     "get",
		Summary:    "Read a jslob settings tree (or one key within it)",
		Keywords:   []string{"preferences", "configuration"},
		Method:     http.MethodPut,
		Endpoint:   "/jslob",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"path": {Type: "string", Required: true, Description: "jslob id, e.g. io.ox/core"},
			"key":  {Type: "string", Required: false, Description: "top-level key to extract; omit to return the whole tree"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"path", "tree", "key", "value"},
			ScalarFields: []string{"path", "key"},
		},
	})
	operations.Register(operations.Operation{
		Name:       "settings.set",
		Resource:   "settings",
		Action:     "set",
		Summary:    "Write a single top-level key in a jslob settings tree. WARNING: this merges into shared account settings that affect live account behavior; only one explicit key is settable at a time, its current value is shown before/after, and the other top-level keys are checked to be unchanged after the write.",
		Method:     http.MethodPut,
		Endpoint:   "/jslob",
		SideEffect: operations.SideEffectWrite,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"path":    {Type: "string", Required: true, Description: "jslob id, e.g. io.ox/mail"},
			"key":     {Type: "string", Required: true, Description: "top-level key to set (no dots; nested paths are not supported by this deployment)"},
			"value":   {Type: "string", Required: true, Description: "new value; parsed as JSON if valid (e.g. true, 5, \"thread\"), otherwise used as a raw string"},
			"dry-run": {Type: "boolean", Required: false, Description: "validate and show what would change, without sending the write"},
		},
		Output: operations.OutputSpec{
			Type:         "object",
			Fields:       []string{"path", "key", "old_value", "new_value"},
			ScalarFields: []string{"path", "key"},
			DryRunFields: []string{"dry_run", "path", "key", "old_value", "new_value"},
		},
	})
}

func runSettings(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("settings requires an action, e.g. \"settings get\""))
	}

	switch args[0] {
	case "get":
		return runSettingsGet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	case "set":
		return runSettingsSet(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
	default:
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown settings action: "+args[0]))
	}
}

// jslobEntry is one element of the jslob?action=list response's "data" array.
type jslobEntry struct {
	ID   string                     `json:"id"`
	Tree map[string]json.RawMessage `json:"tree"`
}

func fetchJSLobTree(ctx context.Context, c *client.Client, sess client.Session, path string) (map[string]json.RawMessage, error) {
	data, err := c.DoJSON(ctx, http.MethodPut, "/jslob", "list", sess, nil, []string{path})
	if err != nil {
		return nil, err
	}

	var entries []jslobEntry
	if err := json.Unmarshal(data, &entries); err != nil || entries == nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode jslob response"}
	}
	var matchedTree map[string]json.RawMessage
	found := false
	for _, e := range entries {
		if e.ID == path {
			if found {
				return nil, &cliErrors.Error{Code: "invalid_response", Message: "jslob response contained duplicate settings trees for " + path}
			}
			found = true
			if e.Tree == nil {
				return nil, &cliErrors.Error{Code: "invalid_response", Message: "jslob response contained a null settings tree for " + path}
			}
			matchedTree = e.Tree
		}
	}
	if found {
		return matchedTree, nil
	}
	// Entries that are all for other ids mean the requested tree was not read. Returning
	// one of them would present another tree's contents under the requested path, and would
	// make a subsequent write report an old value it never read.
	if len(entries) > 0 {
		return nil, &cliErrors.Error{
			Code:    "invalid_response",
			Message: "jslob response did not contain the requested settings tree " + path,
		}
	}
	return map[string]json.RawMessage{}, nil
}

func settingsValue(tree map[string]json.RawMessage, key string) (any, *cliErrors.Error) {
	raw, ok := tree[key]
	if !ok {
		return nil, &cliErrors.Error{Code: "not_found", Message: "settings key not found: " + key}
	}
	value, err := decodeSettingsJSONValue(raw)
	if err != nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode settings value for key " + key}
	}
	return value, nil
}

func decodeSettingsJSONValue(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func runSettingsGet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("settings.get", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	path, key := f.String("path"), f.String("key")

	if path == "" {
		return exitWithError(stderr, llm, invalidArgument("--path is required: a settings tree such as io.ox/core (language, timezone, theme), io.ox/mail (sending, compose, signatures), io.ox/calendar, io.ox/contacts, io.ox/tasks or io.ox/files; mail senders are in \"mbx mail senders\""))
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	tree, err := fetchJSLobTree(ctx, c, sess, path)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	if key != "" {
		value, valueErr := settingsValue(tree, key)
		if valueErr != nil {
			return exitWithError(stderr, llm, valueErr)
		}
		if llm {
			render.CompactJSON(stdout, struct {
				Path  string `json:"path"`
				Key   string `json:"key"`
				Value any    `json:"value"`
			}{Path: path, Key: key, Value: value})
			return 0
		}
		render.KeyValueTable(stdout, []render.Field{
			{Name: "path", Value: path},
			{Name: "key", Value: key},
			{Name: "value", Value: fmt.Sprintf("%v", value)},
		})
		return 0
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Path string                     `json:"path"`
			Tree map[string]json.RawMessage `json:"tree"`
		}{Path: path, Tree: tree})
		return 0
	}

	indented, _ := json.MarshalIndent(tree, "", "  ")
	fmt.Fprintf(stdout, "path: %s\n%s\n", render.SafeText(path), render.SafeJSON(indented))
	return 0
}

func runSettingsSet(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	f, ferr := parseCommandFlags("settings.set", args)
	if ferr != nil {
		return exitWithError(stderr, f.LLM(), ferr)
	}
	llm := f.LLM()
	dryRun := f.Bool("dry-run")
	path, key := f.String("path"), f.String("key")
	value, haveValue := f.Lookup("value")

	if path == "" || key == "" || !haveValue {
		return exitWithError(stderr, llm, invalidArgument("--path, --key, and --value are all required"))
	}
	if strings.Contains(key, ".") {
		return exitWithError(stderr, llm, invalidArgument("--key must be a single top-level key (no dots); this deployment does not support nested path writes"))
	}

	newValue, parseErr := decodeSettingsJSONValue([]byte(value))
	if parseErr != nil {
		newValue = value
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, llm, authErr)
	}

	tree, err := fetchJSLobTree(ctx, c, sess, path)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}

	var oldValue any
	if raw, ok := tree[key]; ok {
		oldValue, _ = decodeSettingsJSONValue(raw)
	}

	if dryRun {
		if llm {
			render.CompactJSON(stdout, struct {
				DryRun   bool   `json:"dry_run"`
				Path     string `json:"path"`
				Key      string `json:"key"`
				OldValue any    `json:"old_value"`
				NewValue any    `json:"new_value"`
			}{DryRun: true, Path: path, Key: key, OldValue: oldValue, NewValue: newValue})
			return 0
		}
		fmt.Fprintf(stdout, "dry-run: no change sent (the current value was read)\npath: %s\nkey: %s\nvalue: %s -> %s\n",
			render.SafeText(path), render.SafeText(key), render.SafeText(fmt.Sprintf("%v", oldValue)), render.SafeText(fmt.Sprintf("%v", newValue)))
		return 0
	}

	// action=update merges the body into the stored tree and changes only the keys it
	// names. action=set would store the body as the whole tree and so could delete every
	// other setting at this path. A merge cannot remove the old subkeys of an object
	// value, so an object value uses the documented {"path","value"} form, which puts the
	// value at that one key.
	var body any = map[string]any{key: newValue}
	if _, isObject := newValue.(map[string]any); isObject {
		if strings.Contains(key, "/") {
			return exitWithError(stderr, llm, invalidArgument("an object --value needs a --key without \"/\"; set the parent key instead"))
		}
		body = map[string]any{"path": key, "value": newValue}
	}
	params := url.Values{"id": {path}}
	if _, err := c.DoJSON(ctx, http.MethodPut, "/jslob", "update", sess, params, body); err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	afterTree, err := fetchJSLobTree(ctx, c, sess, path)
	if err != nil {
		return exitWithError(stderr, llm, toCLIError(err, "api_error"))
	}
	afterValue, afterErr := settingsValue(afterTree, key)
	if afterErr != nil || !reflect.DeepEqual(afterValue, newValue) {
		return exitWithError(stderr, llm, &cliErrors.Error{Code: "invalid_response", Message: "settings write did not apply requested value for key " + key})
	}
	if changed := changedOtherSettingsKeys(tree, afterTree, key); len(changed) > 0 {
		cerr := &cliErrors.Error{
			Code: "invalid_response",
			Message: fmt.Sprintf("settings write set key %s, but other top-level keys in %s are now changed or missing: %s; read the tree with \"mbx settings get --path %s\" and restore them if needed (another client can also have changed them)",
				key, path, strings.Join(changed, ", "), path),
		}
		return exitWithError(stderr, llm, cerr)
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Path     string `json:"path"`
			Key      string `json:"key"`
			OldValue any    `json:"old_value"`
			NewValue any    `json:"new_value"`
		}{Path: path, Key: key, OldValue: oldValue, NewValue: afterValue})
		return 0
	}

	render.KeyValueTable(stdout, []render.Field{
		{Name: "path", Value: path},
		{Name: "key", Value: key},
		{Name: "old_value", Value: fmt.Sprintf("%v", oldValue)},
		{Name: "new_value", Value: fmt.Sprintf("%v", afterValue)},
	})
	return 0
}

// changedOtherSettingsKeys compares every top-level key of the pre-write tree except key
// with the post-write tree and returns, sorted, the keys whose value changed or that are
// missing. A merge write must leave all of them as they were.
func changedOtherSettingsKeys(before, after map[string]json.RawMessage, key string) []string {
	var changed []string
	for name, rawBefore := range before {
		if name == key {
			continue
		}
		rawAfter, ok := after[name]
		if !ok {
			changed = append(changed, name)
			continue
		}
		valueBefore, errBefore := decodeSettingsJSONValue(rawBefore)
		valueAfter, errAfter := decodeSettingsJSONValue(rawAfter)
		if errBefore != nil || errAfter != nil || !reflect.DeepEqual(valueBefore, valueAfter) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}
