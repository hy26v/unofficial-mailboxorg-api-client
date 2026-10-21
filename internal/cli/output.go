// ABOUTME: Global machine-output flag parsing and transformation for every CLI command.
// ABOUTME: Preserves --llm JSON while adding JSONL, TSV, values, and field selection.
package cli

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

type outputOptions struct {
	structured bool
	format     render.StructuredFormat
	selectors  []string
	// changesData is set for an invocation that changes data: a write or destructive
	// operation, or a read given a write flag (mail read --mark-seen), that is not a dry
	// run or preflight. When its result cannot be projected, the change has
	// already happened, so the full result must still reach the caller.
	changesData bool
}

// exitCodeUnprojectedResult is returned when a command that changed data succeeded but
// --select could not be applied to its result. stdout then holds the full result and
// stderr an output_projection_failed error.
const exitCodeUnprojectedResult = 5

const minimumStructuredOutputBytes = 256

// structuredOutputRequested is a best-effort pre-scan used only when full output-option
// parsing fails. It preserves the requested JSON error contract even when the parser
// returns before it has finalized outputOptions.
func structuredOutputRequested(args []string) bool {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--":
			return false
		case args[i] == "--llm", args[i] == "--select", strings.HasPrefix(args[i], "--select="):
			return true
		case args[i] == "--format" && i+1 < len(args):
			value := strings.ToLower(strings.TrimSpace(args[i+1]))
			if value != "human" && value != "table" {
				if _, err := render.ParseFormat(value); err == nil {
					return true
				}
			}
		case strings.HasPrefix(args[i], "--format="):
			value := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(args[i], "--format=")))
			if _, err := render.ParseFormat(value); err == nil {
				return true
			}
		}
	}
	return false
}

func parseOutputOptions(args []string) (outputOptions, []string, *cliErrors.Error) {
	var opts outputOptions
	clean := make([]string, 0, len(args)+1)
	explicitFormat := false
	humanFormat := false
	legacyLLM := false
	seenSelectors := map[string]bool{}
	appendSelectors := func(fields []string) {
		for _, field := range fields {
			if !seenSelectors[field] {
				seenSelectors[field] = true
				opts.selectors = append(opts.selectors, field)
			}
		}
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			// Everything after the conventional option boundary belongs to the command,
			// even when a value happens to have the same spelling as a global output flag.
			clean = append(clean, args[i+1:]...)
			i = len(args)
		case arg == "--llm":
			legacyLLM = true
		case arg == "--format":
			if i+1 >= len(args) {
				return opts, nil, outputArgError("--format requires a value")
			}
			i++
			explicitFormat = true
			if err := applyFormat(&opts, &humanFormat, args[i]); err != nil {
				return opts, nil, err
			}
		case strings.HasPrefix(arg, "--format="):
			explicitFormat = true
			if err := applyFormat(&opts, &humanFormat, strings.TrimPrefix(arg, "--format=")); err != nil {
				return opts, nil, err
			}
		case arg == "--select":
			if i+1 >= len(args) {
				return opts, nil, outputArgError("--select requires a comma-separated field list")
			}
			i++
			fields, err := parseSelectors(args[i])
			if err != nil {
				return opts, nil, outputArgError(err.Error())
			}
			appendSelectors(fields)
		case strings.HasPrefix(arg, "--select="):
			fields, err := parseSelectors(strings.TrimPrefix(arg, "--select="))
			if err != nil {
				return opts, nil, outputArgError(err.Error())
			}
			appendSelectors(fields)
		default:
			clean = append(clean, arg)
		}
	}

	if humanFormat && legacyLLM {
		return opts, nil, outputArgError("--format human cannot be combined with --llm")
	}
	if humanFormat && len(opts.selectors) > 0 {
		return opts, nil, outputArgError("--select requires a structured --format")
	}
	if legacyLLM && !explicitFormat {
		opts.structured = true
		opts.format = render.FormatJSON
	}
	if len(opts.selectors) > 0 && !opts.structured {
		opts.structured = true
		opts.format = render.FormatJSON
	}
	return opts, clean, nil
}

// applyFormat resolves one --format value onto opts. Repeating the same format is
// harmless, but two different ones are a contradiction the CLI cannot honour, so rather
// than let the last flag win and silently discard the other it reports the conflict.
func applyFormat(opts *outputOptions, humanFormat *bool, value string) *cliErrors.Error {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return outputArgError("--format requires a value")
	}

	if value == "human" || value == "table" {
		if opts.structured {
			return conflictingFormatError(string(opts.format), value)
		}
		*humanFormat = true
		return nil
	}

	format, err := render.ParseFormat(value)
	if err != nil {
		return outputArgError(err.Error())
	}
	if *humanFormat {
		return conflictingFormatError("human", string(format))
	}
	if opts.structured && opts.format != format {
		return conflictingFormatError(string(opts.format), string(format))
	}
	opts.structured = true
	opts.format = format
	return nil
}

func conflictingFormatError(first, second string) *cliErrors.Error {
	return outputArgError(fmt.Sprintf("only one --format may be selected, got %q and %q", first, second))
}

func outputArgError(message string) *cliErrors.Error {
	return &cliErrors.Error{Code: "invalid_argument", Message: message}
}

func parseSelectors(value string) ([]string, error) {
	parts := strings.Split(value, ",")
	fields := make([]string, 0, len(parts))
	seen := map[string]bool{}
	for _, part := range parts {
		field := strings.TrimSpace(part)
		if field == "" {
			return nil, fmt.Errorf("--select contains an empty field")
		}
		for _, segment := range strings.Split(field, ".") {
			if segment == "" {
				return nil, fmt.Errorf("invalid selected field %q", field)
			}
		}
		if !seen[field] {
			seen[field] = true
			fields = append(fields, field)
		}
	}
	return fields, nil
}

func validateSelectors(args []string, selectors []string) *cliErrors.Error {
	if len(selectors) == 0 {
		return nil
	}

	known := outputFieldsFor(args)
	if len(known) == 0 {
		return nil
	}
	op, registered, flagStart := operationForArgs(args)
	dryRun := registered && len(op.Output.DryRunFields) > 0 && commandBoolFlagPresent(args, op, flagStart, "dry-run")
	if dryRun {
		known = stringSet(op.Output.DryRunFields...)
	}
	scalars := map[string]bool{}
	if registered && !dryRun {
		scalars = stringSet(op.Output.ScalarFields...)
	}
	for _, selector := range selectors {
		top := strings.Split(selector, ".")[0]
		if !known[top] {
			allowed := make([]string, 0, len(known))
			for field := range known {
				allowed = append(allowed, field)
			}
			sort.Strings(allowed)
			suggestion := closest(top, append([]string(nil), allowed...))
			message := fmt.Sprintf("selected field %q is not documented for this command; allowed fields: %s", selector, strings.Join(allowed, ", "))
			if dryRun {
				message = fmt.Sprintf("selected field %q is not in the --dry-run result of this command; dry-run fields: %s", selector, strings.Join(allowed, ", "))
			}
			if suggestion != "" {
				message += fmt.Sprintf("; did you mean %q?", suggestion)
			} else if known["tree"] && known["key"] && !dryRun {
				// settings get: setting names are keys inside the tree, not output fields.
				message += fmt.Sprintf("; a setting is not an output field: give --key %s for one value, or select tree.%s", top, top)
			}
			return &cliErrors.Error{
				Code:          "invalid_argument",
				Message:       message,
				AllowedFields: allowed,
				Suggestion:    suggestion,
			}
		}
		if selector != top && scalars[top] {
			return &cliErrors.Error{
				Code:          "invalid_argument",
				Message:       fmt.Sprintf("selected field %q goes into %q, which holds a single value and has no fields; select %q instead", selector, top, top),
				AllowedFields: []string{top},
				Suggestion:    top,
			}
		}
	}
	return nil
}

// withSelectedOnRequestFields adds the selected fields that the result holds only on
// request (OutputSpec.OnRequestFields) to the operation's OnRequestArg, so that
// "mail search --select id,to" returns to without --fields to. It changes the value of the
// last given OnRequestArg, which the handler reads, or inserts the flag after the operation
// name, where a trailing flag without its value cannot consume it.
func withSelectedOnRequestFields(args []string, selectors []string) []string {
	op, registered, flagStart := operationForArgs(args)
	if !registered || op.Output.OnRequestArg == "" || len(selectors) == 0 {
		return args
	}
	if _, accepted := op.Args[op.Output.OnRequestArg]; !accepted {
		return args
	}
	valueIndex, current := -1, ""
	scanOperationArgs(args, op, flagStart, func(token flagToken) bool {
		if token.known && token.name == op.Output.OnRequestArg && token.hasValue {
			valueIndex, current = token.index+1, token.value
		}
		return false
	})
	named := map[string]bool{}
	var fields []string
	for _, field := range strings.Split(current, ",") {
		if field = strings.TrimSpace(field); field != "" && !named[field] {
			named[field] = true
			fields = append(fields, field)
		}
	}
	onRequest := stringSet(op.Output.OnRequestFields...)
	added := false
	for _, selector := range selectors {
		top := strings.Split(selector, ".")[0]
		if onRequest[top] && !named[top] {
			named[top] = true
			fields = append(fields, top)
			added = true
		}
	}
	if !added {
		return args
	}
	merged := strings.Join(fields, ",")
	out := append([]string(nil), args...)
	if valueIndex >= 0 {
		out[valueIndex] = merged
		return out
	}
	out = append(out[:flagStart], append([]string{"--" + op.Output.OnRequestArg, merged}, args[flagStart:]...)...)
	return out
}

// validateModeSpecificSelectors rejects projections that the selected execution mode
// cannot emit. This must run before dispatch: structured output is transformed only after
// a handler returns, which is too late for a write command whose mutation already succeeded.
func validateModeSpecificSelectors(args []string, selectors []string) *cliErrors.Error {
	if len(selectors) == 0 {
		return nil
	}
	op, ok, flagStart := operationForArgs(args)
	if !ok {
		return nil
	}

	boolFlag := func(name string) bool {
		return commandBoolFlagPresent(args, op, flagStart, name)
	}
	var available map[string]bool
	switch op.Name {
	case "mail.move":
		dryRun, preflight := boolFlag("dry-run"), boolFlag("preflight")
		if dryRun && preflight {
			return nil // Let the command report the contradictory modes.
		}
		available = stringSet("source", "destination", "verified")
		expectedFingerprint, expectedFingerprintSet := commandStringFlagValue(args, op, flagStart, "expect-fingerprint")
		switch {
		case dryRun:
			available["dry_run"], available["request"] = true, true
			if expectedFingerprintSet && expectedFingerprint != "" {
				available["fingerprint"] = true
			}
		case preflight:
			available["preflight"], available["fingerprint"], available["request"] = true, true, true
		default:
			if boolFlag("verify") || (expectedFingerprintSet && expectedFingerprint != "") {
				available["fingerprint"] = true
			}
		}
	case "folders.create":
		dryRun, preflight := boolFlag("dry-run"), boolFlag("preflight")
		if dryRun && preflight {
			return nil
		}
		switch {
		case dryRun:
			available = stringSet("dry_run", "requested_parent", "parent", "title", "module", "request")
		case preflight:
			available = stringSet("preflight", "valid", "requested_parent", "parent", "title", "module", "request")
		default:
			available = stringSet("id")
		}
	case "folders.ensure":
		dryRun, preflight := boolFlag("dry-run"), boolFlag("preflight")
		if dryRun && preflight {
			return nil
		}
		base := []string{"id", "created", "requested_parent", "parent", "title", "module"}
		switch {
		case dryRun:
			available = stringSet(append(base, "would_create", "dry_run", "request")...)
		case preflight:
			// would_create and request depend on whether the remote child already exists,
			// so they are not guaranteed projections in this mode.
			available = stringSet(append(base, "preflight")...)
		default:
			available = stringSet(base...)
		}
	case "mail.move-many":
		if _, outputSet := commandStringFlagValue(args, op, flagStart, "output"); outputSet {
			return &cliErrors.Error{
				Code:    "invalid_argument",
				Message: "mail move-many item fields cannot be selected with --output; the full item document is written to that file",
			}
		}
		return nil
	default:
		return nil
	}

	for _, selector := range selectors {
		top := strings.Split(selector, ".")[0]
		if available[top] {
			continue
		}
		allowed := make([]string, 0, len(available))
		for field := range available {
			allowed = append(allowed, field)
		}
		sort.Strings(allowed)
		return &cliErrors.Error{
			Code:          "invalid_argument",
			Message:       fmt.Sprintf("selected field %q is not available for this command mode; available fields: %s", selector, strings.Join(allowed, ", ")),
			AllowedFields: allowed,
		}
	}
	return nil
}

func outputFieldsFor(args []string) map[string]bool {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "commands":
		return stringSet("name", "summary", "required", "optional")
	case "describe":
		return stringSet("name", "summary", "usage", "effect", "idempotent", "args", "output", "method", "endpoint", "action", "destructive", "scopes")
	}

	name := args[0]
	if len(args) > 1 && !strings.HasPrefix(args[1], "-") {
		candidate := args[0] + "." + args[1]
		if _, ok := operations.Default.Get(candidate); ok {
			name = candidate
		}
	}
	op, ok := operations.Default.Get(name)
	if !ok {
		return nil
	}
	return stringSet(op.Output.Fields...)
}

func stringSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func runStructured(
	stdout, stderr io.Writer,
	opts outputOptions,
	run func(io.Writer, io.Writer) int,
) int {
	var out bytes.Buffer
	var errOut structuredErrorBuffer
	code := run(&out, &errOut)
	if code != 0 {
		_, _ = io.Copy(stdout, &out)
		_, _ = io.Copy(stderr, &errOut)
		return code
	}

	// Preserve the byte-for-byte legacy JSON contract (including struct field order) when
	// no projection was requested. Transforming through map[string]any would reorder keys
	// and needlessly break scripts and golden contracts.
	// A successful command writes to stderr only for warnings; keep them.
	defer func() { _, _ = io.Copy(stderr, &errOut) }()
	if opts.format == render.FormatJSON && len(opts.selectors) == 0 {
		_, _ = io.Copy(stdout, &out)
		return 0
	}

	var transformed bytes.Buffer
	if err := render.TransformJSON(&transformed, out.Bytes(), opts.format, opts.selectors); err != nil {
		if opts.changesData {
			return writeUnprojectedResult(stdout, stderr, opts, out.Bytes(), err)
		}
		cerr := &cliErrors.Error{
			Code:    "invalid_argument",
			Message: err.Error(),
		}
		_ = render.Error(stderr, cerr, true)
		return cliErrors.ExitCodeFor(cerr)
	}
	_, _ = io.Copy(stdout, &transformed)
	return 0
}

// writeUnprojectedResult handles a --select that fails after a command changed data.
// Suppressing the result would hide what the change produced (for example a new id), so
// stdout gets the full result in the requested format (or the command's own JSON if that
// also fails), and stderr gets an output_projection_failed error.
func writeUnprojectedResult(stdout, stderr io.Writer, opts outputOptions, document []byte, projectionErr error) int {
	var full bytes.Buffer
	if err := render.TransformJSON(&full, document, opts.format, nil); err != nil {
		full.Reset()
		full.Write(document)
	}
	_, _ = io.Copy(stdout, &full)
	cerr := &cliErrors.Error{
		Code:    "output_projection_failed",
		Message: "the command succeeded and its change is done, but --select could not be applied (" + projectionErr.Error() + "); stdout has the full result; do not run the command again",
	}
	_ = render.Error(stderr, cerr, true)
	return exitCodeUnprojectedResult
}

// structuredErrorBuffer tells writeErr that errors produced anywhere inside a structured
// invocation must be JSON, even if a local parser failed before seeing its legacy marker.
type structuredErrorBuffer struct {
	bytes.Buffer
}

func (*structuredErrorBuffer) structuredErrors() {}

// writeTruncationNotice tells a human reader that --limit cut the list short. Every bounded
// list command already computes this to fill the "truncated" field of its machine output; a
// table that stops at the limit and says nothing looks exactly like a complete one, so the
// reader has no way to know more rows exist. It goes to stdout under the table, where the
// rows it describes are, and is written only when rows were actually dropped.
func writeTruncationNotice(w io.Writer, truncated bool, limit int) {
	if !truncated {
		return
	}
	fmt.Fprintf(w, "\n(cut short at the --limit of %d; more rows exist)\n", limit)
}

// requireOutputFileForDocument rejects a document-returning command that was asked for
// machine-readable output but given nowhere to put the document. Commands like events
// export and contacts export stream iCal/vCard bytes to stdout, which is right for a human
// redirecting to a file but is not structured output: it would leave --llm emitting
// something that is not JSON, and --format tsv/jsonl/values failing downstream on a body it
// cannot parse. With --output the command reports the documented saved_to/bytes_written
// object instead, which every format can render.
func requireOutputFileForDocument(structured bool, output, document string) *cliErrors.Error {
	if !structured || output != "" {
		return nil
	}
	return &cliErrors.Error{
		Code:    "invalid_argument",
		Message: "machine-readable output requires --output <file>: " + document + " content is written to that file and the command then reports saved_to and bytes_written",
	}
}
