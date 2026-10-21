// ABOUTME: Safe typed batching for a deliberately small allow-list of registered read operations.
// ABOUTME: Provider responses are decoded and projected before output; raw mail bodies never pass through.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/requestguard"
)

const (
	maxBatchSubrequests     = 20
	defaultBatchOutputBytes = 1 << 20
	maximumBatchOutputBytes = 16 << 20
)

func init() {
	registerResource("batch", runBatch)
	operations.Register(operations.Operation{
		Name:       "batch.run",
		Resource:   "batch",
		Action:     "run",
		Summary:    "Execute supported read operations as one safe typed multiple call",
		Keywords:   []string{"several", "multiple", "parallel"},
		Method:     http.MethodPut,
		Endpoint:   "/multiple",
		SideEffect: operations.SideEffectRead,
		Idempotent: true,
		Args: map[string]operations.ArgSpec{
			"input":            {Type: "string", Required: true, Description: "@file or JSON array of 1-20 registered read operations"},
			"dry-run":          {Type: "bool", Required: false, Description: "validate and show the exact normalized requests without sending them"},
			"output":           {Type: "string", Required: false, Description: "write the full result to an owner-only file instead of stdout"},
			"max-output-bytes": {Type: "int", Required: false, Description: "maximum result size written to stdout (default 1048576, max 16777216)"},
		},
		Output: operations.OutputSpec{
			Type:         "array",
			Fields:       []string{"operation", "result", "error"},
			ScalarFields: []string{"operation"},
			DryRunFields: []string{"operation", "request", "select"},
		},
	})
}

// batchRequest accepts canonical {operation,params,select} requests. Resource/action is
// retained as a compatibility spelling, but is resolved through the operation registry;
// it is never forwarded directly to the provider.
type batchRequest struct {
	Operation string         `json:"operation,omitempty"`
	Resource  string         `json:"resource,omitempty"`
	Action    string         `json:"action,omitempty"`
	Params    map[string]any `json:"params,omitempty"`
	Select    []string       `json:"select,omitempty"`
}

type batchRunOptions struct {
	llm            bool
	dryRun         bool
	input          string
	output         string
	maxOutputBytes int
}

func parseBatchRunOptions(args []string) (batchRunOptions, *cliErrors.Error) {
	f, ferr := parseCommandFlags("batch.run", args,
		intFlag("max-output-bytes", minimumStructuredOutputBytes, maximumBatchOutputBytes,
			fmt.Sprintf("--max-output-bytes must be an integer between %d and %d", minimumStructuredOutputBytes, maximumBatchOutputBytes)))
	opts := batchRunOptions{llm: f.LLM(), maxOutputBytes: defaultBatchOutputBytes}
	if ferr != nil {
		return opts, ferr
	}
	opts.dryRun = f.Bool("dry-run")
	opts.input = f.String("input")
	opts.output = f.String("output")
	if value, given := f.Int("max-output-bytes"); given {
		opts.maxOutputBytes = value
	}
	if opts.input == "" {
		return opts, invalidArgument("--input is required, e.g. --input @requests.json")
	}
	if strings.TrimSpace(opts.output) == "" && opts.output != "" {
		return opts, invalidArgument("--output must not be blank")
	}
	return opts, nil
}

func parseBatchInput(raw string) ([]batchRequest, error) {
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

	var reqs []batchRequest
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reqs); err != nil {
		return nil, fmt.Errorf("--input is not a valid JSON array of sub-requests: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("--input is not a valid JSON array of sub-requests: multiple JSON values")
		}
		return nil, fmt.Errorf("--input is not a valid JSON array of sub-requests: %w", err)
	}
	if reqs == nil {
		return nil, fmt.Errorf("batch input must be a JSON array, not null")
	}
	return reqs, nil
}

type batchPlan struct {
	Operation string
	Provider  map[string]any
	Select    []string
	Decode    func(json.RawMessage) (any, *cliErrors.Error)
}

type batchPreview struct {
	Operation string         `json:"operation"`
	Request   map[string]any `json:"request"`
	Select    []string       `json:"select,omitempty"`
}

func supportedBatchOperations() []string {
	return []string{"mail.get", "mail.list", "quota.get", "whoami"}
}

func canonicalBatchOperation(req batchRequest) (string, map[string]any, error) {
	operation := strings.TrimSpace(req.Operation)
	resource := normalizeBatchResource(req.Resource)
	action := strings.ToLower(strings.TrimSpace(req.Action))
	params := make(map[string]any, len(req.Params)+1)
	for key, value := range req.Params {
		params[key] = value
	}

	if operation != "" {
		if resource != "" || action != "" {
			return "", nil, fmt.Errorf("use either \"operation\" or \"resource\"/\"action\", not both")
		}
	} else {
		if resource == "" {
			return "", nil, fmt.Errorf("missing required field \"operation\" (or compatibility field \"resource\")")
		}
		if action == "" {
			return "", nil, fmt.Errorf("missing required field \"action\"")
		}
		switch resource + "." + action {
		case "user.get":
			operation = "whoami"
		case "mail.all":
			operation = "mail.list"
		case "quota.filestore":
			operation = "quota.get"
			if _, exists := params["type"]; !exists {
				params["type"] = "filestore"
			}
		default:
			operation = resource + "." + action
		}
	}
	if operation == "user.get" {
		operation = "whoami"
	}
	return operation, params, nil
}

func validateBatchParams(index int, op operations.Operation, params map[string]any) error {
	provided := make([]string, 0, len(params))
	for name := range params {
		provided = append(provided, name)
	}
	sort.Strings(provided)
	for _, name := range provided {
		spec, ok := op.Args[name]
		if !ok {
			return fmt.Errorf("sub-request %d %s does not accept param %q", index, op.Name, name)
		}
		value := params[name]
		switch spec.Type {
		case "string":
			if _, ok := value.(string); !ok {
				return fmt.Errorf("sub-request %d %s param %q must be a string", index, op.Name, name)
			}
		case "int":
			if _, err := batchInt(value); err != nil {
				return fmt.Errorf("sub-request %d %s param %q must be an integer", index, op.Name, name)
			}
		case "bool", "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("sub-request %d %s param %q must be a boolean", index, op.Name, name)
			}
		}
	}
	required := make([]string, 0, len(op.Args))
	for name, spec := range op.Args {
		if spec.Required {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	for _, name := range required {
		if _, exists := params[name]; !exists {
			return fmt.Errorf("sub-request %d %s requires param %q", index, op.Name, name)
		}
	}
	return nil
}

func batchInt(value any) (int, error) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseInt(typed.String(), 10, 32)
		return int(parsed), err
	case float64:
		if typed != float64(int(typed)) {
			return 0, fmt.Errorf("not an integer")
		}
		return int(typed), nil
	case int:
		return typed, nil
	default:
		return 0, fmt.Errorf("not an integer")
	}
}

func validateBatchSelect(index int, op operations.Operation, selectors []string) error {
	allowed := append([]string(nil), op.Output.Fields...)
	sort.Strings(allowed)
	known := stringSet(allowed...)
	for _, raw := range selectors {
		selector := strings.TrimSpace(raw)
		if selector == "" || strings.Contains(selector, ".") || !known[selector] {
			return fmt.Errorf("sub-request %d %s has invalid select field %q; allowed fields: %s", index, op.Name, raw, strings.Join(allowed, ", "))
		}
	}
	return nil
}

// validateBatchSelectRequested rejects a select field that the planned request does not
// fetch. mail.list returns its optional fields only when "fields" names them, so without
// this check the batch would send the request and then fail to project the result.
func validateBatchSelectRequested(index int, op operations.Operation, params map[string]any, selectors []string) error {
	if op.Name != "mail.list" || len(selectors) == 0 {
		return nil
	}
	available := stringSet("id", "subject", "from", "date")
	if value, ok := params["fields"].(string); ok {
		fields, err := parseMailListFields(value)
		if err != nil {
			return fmt.Errorf("sub-request %d: %w", index, err)
		}
		for _, field := range fields {
			available[field] = true
		}
	}
	for _, raw := range selectors {
		selector := strings.TrimSpace(raw)
		if !available[selector] {
			return fmt.Errorf("sub-request %d mail.list selects %q, which mail.list returns only when \"fields\" includes it; add %q to \"fields\"", index, selector, selector)
		}
	}
	return nil
}

func planBatchRequests(reqs []batchRequest) ([]batchPlan, error) {
	if len(reqs) == 0 {
		return nil, fmt.Errorf("batch input is empty; provide at least one sub-request")
	}
	if len(reqs) > maxBatchSubrequests {
		return nil, fmt.Errorf("batch input contains %d sub-requests; maximum is %d so one /multiple call cannot bypass burst protection", len(reqs), maxBatchSubrequests)
	}

	plans := make([]batchPlan, len(reqs))
	for i, req := range reqs {
		name, params, err := canonicalBatchOperation(req)
		if err != nil {
			return nil, fmt.Errorf("sub-request %d: %w", i, err)
		}
		op, registered := operations.Default.Get(name)
		if !registered {
			return nil, fmt.Errorf("sub-request %d references unknown operation %q; use a name from mbx commands", i, name)
		}
		if op.SideEffect != operations.SideEffectRead || !op.Idempotent {
			return nil, fmt.Errorf("sub-request %d %s is not read-only; batch.run rejects all mutations", i, op.Name)
		}
		if err := validateBatchParams(i, op, params); err != nil {
			return nil, err
		}
		if err := validateBatchSelect(i, op, req.Select); err != nil {
			return nil, err
		}

		plan, err := buildBatchPlan(op, params)
		if err != nil {
			return nil, fmt.Errorf("sub-request %d: %w", i, err)
		}
		if err := validateBatchSelectRequested(i, op, params, req.Select); err != nil {
			return nil, err
		}
		plan.Select = make([]string, len(req.Select))
		for selectorIndex, selector := range req.Select {
			plan.Select[selectorIndex] = strings.TrimSpace(selector)
		}
		plans[i] = plan
	}
	return plans, nil
}

func buildBatchPlan(op operations.Operation, params map[string]any) (batchPlan, error) {
	supported := strings.Join(supportedBatchOperations(), ", ")
	switch op.Name {
	case "quota.get":
		quotaType := strings.TrimSpace(params["type"].(string))
		if quotaType != "filestore" {
			return batchPlan{}, fmt.Errorf("quota.get supports only type \"filestore\"")
		}
		return batchPlan{
			Operation: op.Name,
			Provider:  map[string]any{"module": "quota", "action": "filestore"},
			Decode:    decodeBatchQuota,
		}, nil
	case "whoami":
		return batchPlan{
			Operation: op.Name,
			Provider:  map[string]any{"module": "user", "action": "get"},
			Decode:    decodeBatchWhoami,
		}, nil
	case "mail.get":
		id := strings.TrimSpace(params["id"].(string))
		folder := strings.TrimSpace(params["folder"].(string))
		if id == "" || folder == "" {
			return batchPlan{}, fmt.Errorf("mail.get id and folder must not be blank")
		}
		requestData, err := json.Marshal([]map[string]string{{"id": id, "folder": folder}})
		if err != nil {
			return batchPlan{}, fmt.Errorf("could not encode mail.get request: %w", err)
		}
		return batchPlan{
			Operation: op.Name,
			Provider: map[string]any{
				"module": "mail", "action": "list", "columns": mailGetColumns, "data": string(requestData),
				"timezone": mailDateTimezone,
			},
			Decode: func(data json.RawMessage) (any, *cliErrors.Error) {
				return decodeBatchMailGet(data, id, folder)
			},
		}, nil
	case "mail.list":
		for name := range params {
			if name != "folder" && name != "limit" && name != "fields" {
				return batchPlan{}, fmt.Errorf("mail.list batching supports only folder, limit, and fields; use direct mail.list for pagination, recursion, or filters")
			}
		}
		opts := mailListOptions{folder: mailListDefaultFolder, limit: mailListDefaultLimit}
		if value, ok := params["folder"]; ok {
			opts.folder = strings.TrimSpace(value.(string))
		}
		if value, ok := params["limit"]; ok {
			limit, err := batchInt(value)
			if err != nil || limit < 1 || limit > mailListMaxLimit {
				return batchPlan{}, fmt.Errorf("mail.list limit must be between 1 and %d", mailListMaxLimit)
			}
			opts.limit = limit
		}
		if value, ok := params["fields"]; ok {
			fields, err := parseMailListFields(value.(string))
			if err != nil {
				return batchPlan{}, err
			}
			opts.fields = fields
		}
		if opts.folder == "" {
			return batchPlan{}, fmt.Errorf("mail.list folder must not be blank")
		}
		columns := mailListColumnsFor(opts)
		columnIDs := make([]string, len(columns))
		for i := range columns {
			columnIDs[i] = columns[i].ID
		}
		return batchPlan{
			Operation: op.Name,
			Provider: map[string]any{
				"module": "mail", "action": "all", "folder": opts.folder,
				"columns": strings.Join(columnIDs, ","), "sort": "610", "order": "desc",
				"left_hand_limit": 0, "right_hand_limit": opts.limit + 1, "timezone": mailDateTimezone,
			},
			Decode: func(data json.RawMessage) (any, *cliErrors.Error) {
				var rows [][]json.RawMessage
				if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
					return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode batched mail.list response"}
				}
				if len(rows) > opts.limit {
					rows = rows[:opts.limit]
				}
				items := make([]mailListItem, 0, len(rows))
				for _, row := range rows {
					item, err := decodeMailListRowWithColumns(row, columns, opts)
					if err != nil {
						return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode batched mail.list row: " + err.Error()}
					}
					if requestedMailListField(opts, "folder") {
						item.Folder = opts.folder
					}
					items = append(items, item)
				}
				return items, nil
			},
		}, nil
	default:
		return batchPlan{}, fmt.Errorf("registered read operation %s is not batchable; supported operations: %s", op.Name, supported)
	}
}

type batchQuotaResult struct {
	Quota int64 `json:"quota"`
	Use   int64 `json:"use"`
}

func decodeBatchQuota(data json.RawMessage) (any, *cliErrors.Error) {
	var decoded struct {
		Quota *int64 `json:"quota"`
		Use   *int64 `json:"use"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil || decoded.Quota == nil || decoded.Use == nil || *decoded.Quota < 0 || *decoded.Use < 0 {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode batched quota.get response"}
	}
	return batchQuotaResult{Quota: *decoded.Quota, Use: *decoded.Use}, nil
}

type batchWhoamiResult struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	UserID      int    `json:"user_id"`
}

func decodeBatchWhoami(data json.RawMessage) (any, *cliErrors.Error) {
	var decoded struct {
		Email       string `json:"email1"`
		DisplayName string `json:"display_name"`
		UserID      *int   `json:"user_id"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil || strings.TrimSpace(decoded.Email) == "" || decoded.UserID == nil || *decoded.UserID <= 0 {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode batched whoami response"}
	}
	return batchWhoamiResult{Email: decoded.Email, DisplayName: decoded.DisplayName, UserID: *decoded.UserID}, nil
}

func decodeBatchMailGet(data json.RawMessage, id, folder string) (any, *cliErrors.Error) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not decode batched mail.get response"}
	}
	if len(rows) == 0 {
		return nil, &cliErrors.Error{Code: "not_found", Message: "message not found: id=" + id + " folder=" + folder}
	}
	if len(rows) != 1 {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "batched mail.get response contained multiple messages"}
	}
	result, err := decodeMailGetRow(rows[0])
	if err != nil || result.ID != id || result.Folder != folder {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "batched mail.get response did not match the requested message"}
	}
	return result, nil
}

func projectBatchResult(value any, selectors []string) (any, *cliErrors.Error) {
	if len(selectors) == 0 {
		return value, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, &cliErrors.Error{Code: "invalid_response", Message: "could not project batch result"}
	}
	projectObject := func(object map[string]json.RawMessage) (map[string]json.RawMessage, *cliErrors.Error) {
		projected := make(map[string]json.RawMessage, len(selectors))
		for _, selector := range selectors {
			value, ok := object[selector]
			if !ok {
				return nil, &cliErrors.Error{Code: "invalid_response", Message: "batch result omitted selected field " + selector}
			}
			projected[selector] = value
		}
		return projected, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err == nil && object != nil {
		return projectObject(object)
	}
	var objects []map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &objects); err == nil && objects != nil {
		projected := make([]map[string]json.RawMessage, len(objects))
		for i := range objects {
			row, projectionErr := projectObject(objects[i])
			if projectionErr != nil {
				return nil, projectionErr
			}
			projected[i] = row
		}
		return projected, nil
	}
	return nil, &cliErrors.Error{Code: "invalid_response", Message: "batch result was neither an object nor an array of objects"}
}

type batchResultEntry struct {
	Operation string           `json:"operation"`
	Result    any              `json:"result,omitempty"`
	Error     *cliErrors.Error `json:"error,omitempty"`
}

type batchResultDocument struct {
	Results []batchResultEntry `json:"results"`
}

func runBatch(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if len(args) == 0 {
		return exitWithError(stderr, false, invalidArgument("batch requires an action, e.g. \"batch run\""))
	}
	if args[0] != "run" {
		return exitWithError(stderr, hasFlag(args, "--llm"), invalidArgument("unknown batch action: "+args[0]))
	}
	return runBatchRun(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
}

func runBatchRun(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	opts, optionErr := parseBatchRunOptions(args)
	if optionErr != nil {
		return exitWithError(stderr, opts.llm, optionErr)
	}
	reqs, parseErr := parseBatchInput(opts.input)
	if parseErr != nil {
		return exitWithError(stderr, opts.llm, invalidArgument(parseErr.Error()))
	}
	plans, planErr := planBatchRequests(reqs)
	if planErr != nil {
		return exitWithError(stderr, opts.llm, invalidArgument(planErr.Error()))
	}

	if opts.dryRun {
		previews := make([]batchPreview, len(plans))
		for i, plan := range plans {
			previews[i] = batchPreview{Operation: plan.Operation, Request: plan.Provider, Select: plan.Select}
		}
		document := struct {
			DryRun   bool           `json:"dry_run"`
			Requests []batchPreview `json:"requests"`
		}{DryRun: true, Requests: previews}
		return emitBatchDocument(stdout, stderr, opts, document, func() {
			rows := make([][]string, len(previews))
			for i, preview := range previews {
				rows[i] = []string{preview.Operation, fmt.Sprint(preview.Request["module"]), fmt.Sprint(preview.Request["action"])}
			}
			render.KeyValueTable(stdout, []render.Field{{Name: "dry_run", Value: "true"}})
			render.Table(stdout, []string{"operation", "module", "action"}, rows)
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	c, sess, authErr := signIn(ctx, httpClient, baseURL, sessionPath, getenv)
	if authErr != nil {
		return exitWithError(stderr, opts.llm, authErr)
	}

	requests := make([]map[string]any, len(plans))
	for i := range plans {
		requests[i] = plans[i].Provider
	}
	batchCtx := requestguard.WithRequestCost(ctx, len(requests))
	data, err := c.DoJSONRaw(batchCtx, http.MethodPut, "/multiple", "", sess, nil, requests)
	if err != nil {
		return exitWithError(stderr, opts.llm, toCLIError(err, "api_error"))
	}

	var rawResults []json.RawMessage
	if err := json.Unmarshal(data, &rawResults); err != nil || rawResults == nil {
		return exitWithError(stderr, opts.llm, &cliErrors.Error{Code: "invalid_response", Message: "could not decode batch response"})
	}
	if len(rawResults) != len(plans) {
		return exitWithError(stderr, opts.llm, &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("batch response has %d results, want %d", len(rawResults), len(plans))})
	}

	// One sub-result that cannot be decoded or projected becomes that entry's error, like
	// a provider error envelope, so the other results are kept.
	entries := make([]batchResultEntry, len(plans))
	for i, raw := range rawResults {
		entries[i].Operation = plans[i].Operation
		if cliErrors.IsErrorEnvelope(raw) {
			var envelope cliErrors.Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				entries[i].Error = &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("could not decode batch error response at index %d", i)}
				continue
			}
			entries[i].Error = cliErrors.FromEnvelope(envelope, http.StatusOK)
			continue
		}
		var response struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &response); err != nil || response.Data == nil {
			entries[i].Error = &cliErrors.Error{Code: "invalid_response", Message: fmt.Sprintf("batch response at index %d did not include data", i)}
			continue
		}
		decoded, decodeErr := plans[i].Decode(response.Data)
		if decodeErr != nil {
			entries[i].Error = decodeErr
			continue
		}
		projected, projectionErr := projectBatchResult(decoded, plans[i].Select)
		if projectionErr != nil {
			entries[i].Error = projectionErr
			continue
		}
		entries[i].Result = projected
	}

	document := batchResultDocument{Results: entries}
	return emitBatchDocument(stdout, stderr, opts, document, func() {
		rows := make([][]string, len(entries))
		for i, entry := range entries {
			var value any = entry.Result
			if entry.Error != nil {
				value = entry.Error
			}
			encoded, _ := json.Marshal(value)
			rows[i] = []string{entry.Operation, string(encoded)}
		}
		render.Table(stdout, []string{"operation", "result"}, rows)
	})
}

func emitBatchDocument(stdout, stderr io.Writer, opts batchRunOptions, document any, renderHuman func()) int {
	payload, err := json.Marshal(document)
	if err != nil {
		return exitWithError(stderr, opts.llm, &cliErrors.Error{Code: "invalid_response", Message: "could not encode batch output"})
	}
	payload = append(payload, '\n')
	if opts.output != "" {
		if err := writePrivateFile(opts.output, payload); err != nil {
			return exitWithError(stderr, opts.llm, invalidArgument("could not write --output file: "+err.Error()))
		}
		if opts.llm {
			render.CompactJSON(stdout, struct {
				SavedTo      string `json:"saved_to"`
				BytesWritten int    `json:"bytes_written"`
			}{SavedTo: opts.output, BytesWritten: len(payload)})
		} else {
			render.KeyValueTable(stdout, []render.Field{{Name: "saved_to", Value: opts.output}, {Name: "bytes_written", Value: strconv.Itoa(len(payload))}})
		}
		return 0
	}
	if len(payload) > opts.maxOutputBytes {
		return exitWithError(stderr, opts.llm, &cliErrors.Error{Code: "output_too_large", Message: fmt.Sprintf("batch output is %d bytes, exceeding --max-output-bytes=%d; use --output <file> for owner-only storage", len(payload), opts.maxOutputBytes)})
	}
	if opts.llm {
		_, _ = stdout.Write(payload)
	} else {
		renderHuman()
	}
	return 0
}
