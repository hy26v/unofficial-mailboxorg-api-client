// ABOUTME: CLI entry point logic: parses arguments, dispatches to commands, and wires
// ABOUTME: authentication, transport, and human/LLM rendering.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/config"
	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

const (
	clientName     = "mbx"
	requestTimeout = 30 * time.Second
)

// Handler runs one top-level resource command (e.g. everything after "whoami" or "quota").
// It is the shape every resource file's init() registers into resourceHandlers, so Run()
// never needs a hand-written case for each resource.
type Handler func(stdout, stderr io.Writer, args []string, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int

// PasswordPrompt obtains a password for one explicit interactive login. The process
// entry point supplies it only when stdin is a terminal; library and non-interactive
// callers keep using MBX_PASSWORD and never block waiting for input.
type PasswordPrompt func() (string, error)

// UsernamePrompt obtains the durable, non-secret mailbox address during first-run login.
type UsernamePrompt func() (string, error)

// LoginPrompts contains terminal-only inputs for explicit interactive login. Secret,
// when set, reads other hidden values such as the passwords for change-password.
type LoginPrompts struct {
	Username UsernamePrompt
	Password PasswordPrompt
	Secret   SecretPrompt
}

// resourceHandlers maps a top-level command name (the resource) to its handler. Each
// resource's own file populates this from an init() function via registerResource, so
// adding a resource never requires editing Run().
var resourceHandlers = map[string]Handler{}

// registerResource registers the handler for a top-level command name. Call it from an
// init() function in the resource's own file (e.g. internal/cli/contacts.go).
func registerResource(name string, h Handler) {
	resourceHandlers[name] = h
}

// Run parses args and executes the requested command, writing to stdout/stderr and
// returning a process exit code. Runtime paths, environment access, and the HTTP client
// are injected so tests never touch the real network or user configuration.
func Run(
	stdout, stderr io.Writer,
	args []string,
	httpClient *http.Client,
	baseURL, configPath, defaultSessionPath string,
	getenv func(string) string,
) int {
	return runCLI(stdout, stderr, args, httpClient, baseURL, configPath, defaultSessionPath, getenv, LoginPrompts{})
}

// RunWithPasswordPrompt is Run with an optional interactive password source. The prompt
// is considered only for a valid explicit "login" command when MBX_PASSWORD is absent.
func RunWithPasswordPrompt(
	stdout, stderr io.Writer,
	args []string,
	httpClient *http.Client,
	baseURL, configPath, defaultSessionPath string,
	getenv func(string) string,
	passwordPrompt PasswordPrompt,
) int {
	return runCLI(stdout, stderr, args, httpClient, baseURL, configPath, defaultSessionPath, getenv, LoginPrompts{Password: passwordPrompt})
}

// RunWithLoginPrompts is Run with terminal-only first-run username and password inputs.
func RunWithLoginPrompts(
	stdout, stderr io.Writer,
	args []string,
	httpClient *http.Client,
	baseURL, configPath, defaultSessionPath string,
	getenv func(string) string,
	loginPrompts LoginPrompts,
) int {
	return runCLI(stdout, stderr, args, httpClient, baseURL, configPath, defaultSessionPath, getenv, loginPrompts)
}

func runCLI(
	stdout, stderr io.Writer,
	args []string,
	httpClient *http.Client,
	baseURL, configPath, defaultSessionPath string,
	getenv func(string) string,
	loginPrompts LoginPrompts,
) int {
	output, cleanArgs, outputErr := parseOutputOptions(args)
	if outputErr != nil {
		writeErr(stderr, output.structured || structuredOutputRequested(args), outputErr)
		return cliErrors.ExitCodeFor(outputErr)
	}

	if target, ok := helpRequest(cleanArgs); ok {
		return runHelp(stdout, stderr, target)
	}
	if isVersionRequest(cleanArgs) {
		return runVersion(stdout, stderr, cleanArgs[1:], output)
	}

	if err := validateSelectors(cleanArgs, output.selectors); err != nil {
		writeErr(stderr, output.structured, err)
		return cliErrors.ExitCodeFor(err)
	}
	if err := validateModeSpecificSelectors(cleanArgs, output.selectors); err != nil {
		writeErr(stderr, output.structured, err)
		return cliErrors.ExitCodeFor(err)
	}
	cleanArgs = withSelectedOnRequestFields(cleanArgs, output.selectors)

	if op, registered, flagStart := operationForArgs(cleanArgs); registered {
		output.changesData = (op.SideEffect != operations.SideEffectRead || commandWriteFlag(cleanArgs, op, flagStart) != "") &&
			!commandRequestsDryRun(cleanArgs, op, flagStart)
	}

	dispatchArgs := cleanArgs
	if output.structured && commandAcceptsLegacyLLM(cleanArgs) {
		// Handlers still use the legacy --llm flag to select their JSON renderer. Add one
		// normalized internal marker only for a complete, known operation. Keep it next to
		// the operation name: appending it after the user's flags would let a trailing flag
		// with a missing value consume the marker as that value.
		dispatchArgs = addLegacyLLMFlag(cleanArgs)
	}

	// Command discovery is entirely local and is also the tool callers use to understand
	// how to repair an invocation. A malformed user config must not make "commands" and
	// "describe" unavailable when neither command reads configuration or mutable state.
	if isLocalDiscoveryCommand(cleanArgs) {
		if output.structured {
			return runStructured(stdout, stderr, output, func(out, errOut io.Writer) int {
				return runDispatch(out, errOut, dispatchArgs, nil, httpClient, baseURL, defaultSessionPath, getenv)
			})
		}
		return runDispatch(stdout, stderr, dispatchArgs, nil, httpClient, baseURL, defaultSessionPath, getenv)
	}

	settings, loadErr := config.Load(configPath)
	if loadErr != nil {
		err := &cliErrors.Error{Code: "invalid_configuration", Message: loadErr.Error()}
		writeErr(stderr, output.structured, err)
		return cliErrors.ExitCodeFor(err)
	}
	sessionPath, pathErr := settings.ResolveSessionFile(configPath, defaultSessionPath)
	if pathErr != nil {
		err := &cliErrors.Error{Code: "invalid_configuration", Message: pathErr.Error()}
		writeErr(stderr, output.structured, err)
		return cliErrors.ExitCodeFor(err)
	}
	var preparedMoveMany *preparedMailMoveMany
	if len(cleanArgs) >= 2 && cleanArgs[0] == "mail" && cleanArgs[1] == "move-many" {
		prepared, preparationErr := prepareMailMoveMany(cleanArgs[2:])
		if preparationErr != nil {
			writeErr(stderr, output.structured, preparationErr)
			return cliErrors.ExitCodeFor(preparationErr)
		}
		prepared.options.llm = output.structured
		preparedMoveMany = &prepared
	}
	if err := commandReadOnlyError(cleanArgs, settings.Safety.ReadOnly, getenv); err != nil {
		writeErr(stderr, output.structured, err)
		return cliErrors.ExitCodeFor(err)
	}
	if err := commandDeleteProtectionError(cleanArgs, settings.Safety.DeleteProtection, preparedMoveMany); err != nil {
		writeErr(stderr, output.structured, err)
		return cliErrors.ExitCodeFor(err)
	}
	// batch run needs no delete-protection check of its own: planBatchRequests rejects
	// every operation that is not a registered read before authentication, so no batch
	// sub-request can delete anything (see TestBatchPlanning_RejectsEveryNonReadOperation).

	effectiveGetenv, promptErr := withPromptedLoginCredentials(cleanArgs, settings, configPath, getenv, loginPrompts)
	if promptErr != nil {
		writeErr(stderr, output.structured, promptErr)
		return cliErrors.ExitCodeFor(promptErr)
	}
	effectiveGetenv, promptErr = withPromptedChangePasswordSecrets(cleanArgs, effectiveGetenv, loginPrompts.Secret, stderr, output.structured)
	if promptErr != nil {
		writeErr(stderr, output.structured, promptErr)
		return cliErrors.ExitCodeFor(promptErr)
	}

	if output.structured {
		return runStructured(stdout, stderr, output, func(out, errOut io.Writer) int {
			return runDispatch(out, errOut, dispatchArgs, preparedMoveMany, httpClient, baseURL, sessionPath, effectiveGetenv)
		})
	}

	return runDispatch(stdout, stderr, dispatchArgs, preparedMoveMany, httpClient, baseURL, sessionPath, effectiveGetenv)
}

func addLegacyLLMFlag(args []string) []string {
	insertAt := 1
	if len(args) > 1 {
		if _, ok := operations.Default.Get(args[0] + "." + args[1]); ok {
			insertAt = 2
		}
	}
	withMarker := make([]string, 0, len(args)+1)
	withMarker = append(withMarker, args[:insertAt]...)
	withMarker = append(withMarker, "--llm")
	withMarker = append(withMarker, args[insertAt:]...)
	return withMarker
}

func isLocalDiscoveryCommand(args []string) bool {
	return len(args) > 0 && (args[0] == "commands" || args[0] == "describe")
}

func commandAcceptsLegacyLLM(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "commands" || args[0] == "describe" {
		return true
	}
	if _, ok := operations.Default.Get(args[0]); ok {
		return true
	}
	if len(args) < 2 {
		return false
	}
	_, ok := operations.Default.Get(args[0] + "." + args[1])
	return ok
}

func runDispatch(stdout, stderr io.Writer, args []string, preparedMoveMany *preparedMailMoveMany, httpClient *http.Client, baseURL, sessionPath string, getenv func(string) string) int {
	if preparedMoveMany != nil && len(args) >= 2 && args[0] == "mail" && args[1] == "move-many" {
		return runPreparedMailMoveMany(stdout, stderr, *preparedMoveMany, httpClient, baseURL, sessionPath, getenv)
	}
	switch args[0] {
	case "commands":
		return runCommands(stdout, stderr, args[1:])
	case "describe":
		return runDescribe(stdout, stderr, args[1:])
	default:
		if h, ok := resourceHandlers[args[0]]; ok {
			return h(stdout, stderr, args[1:], httpClient, baseURL, sessionPath, getenv)
		}
		llm := hasFlag(args, "--llm")
		message := "unknown command: " + args[0]
		if suggestion := suggestCommand(args[0]); suggestion != "" {
			message += fmt.Sprintf("; did you mean %q?", suggestion)
		}
		err := &cliErrors.Error{Code: "invalid_argument", Message: message}
		writeErr(stderr, llm, err)
		return cliErrors.ExitCodeFor(err)
	}
}

func usageFor(op operations.Operation) string {
	usage := clientName + " " + strings.ReplaceAll(op.Name, ".", " ")
	for _, name := range sortedArgNames(op.Args) {
		arg := op.Args[name]
		if arg.Required {
			usage += " --" + name + " <" + arg.Type + ">"
		}
	}
	return usage
}

func sortedArgNames(args map[string]operations.ArgSpec) []string {
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func runCommands(stdout, stderr io.Writer, args []string) int {
	llm := false
	resource := ""
	search, searchSet := "", false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--llm":
			llm = true
		case a == "--search" || strings.HasPrefix(a, "--search="):
			if value, ok := strings.CutPrefix(a, "--search="); ok {
				search = value
			} else if i+1 < len(args) {
				i++
				search = args[i]
			}
			searchSet = true
			if strings.TrimSpace(search) == "" {
				err := &cliErrors.Error{Code: "invalid_argument", Message: "--search needs words, for example --search \"sender address\""}
				writeErr(stderr, llm, err)
				return cliErrors.ExitCodeFor(err)
			}
		case strings.HasPrefix(a, "--"):
			err := &cliErrors.Error{Code: "invalid_argument", Message: "unknown flag: " + a}
			writeErr(stderr, llm, err)
			return cliErrors.ExitCodeFor(err)
		case resource == "":
			resource = a
		default:
			err := &cliErrors.Error{Code: "invalid_argument", Message: "unexpected argument: " + a}
			writeErr(stderr, llm, err)
			return cliErrors.ExitCodeFor(err)
		}
	}

	ops := operations.Default.List()
	type cmdSummary struct {
		Name     string   `json:"name"`
		Summary  string   `json:"summary"`
		Required []string `json:"required,omitempty"`
		Optional []string `json:"optional,omitempty"`
	}
	var cmds []cmdSummary
	if searchSet {
		keep := func(op operations.Operation) bool { return resource == "" || op.Resource == resource }
		for _, op := range operations.Default.Search(search, 10, keep) {
			required, optional := op.RequiredAndOptionalArgs(nil)
			cmds = append(cmds, cmdSummary{Name: op.Name, Summary: op.Summary, Required: required, Optional: optional})
		}
		if cmds == nil {
			cmds = []cmdSummary{}
		}
	} else if resource != "" {
		for _, op := range ops {
			if op.Resource == resource {
				cmds = append(cmds, cmdSummary{Name: op.Name, Summary: op.Summary})
			}
		}
		if len(cmds) == 0 {
			err := &cliErrors.Error{
				Code:    "invalid_argument",
				Message: fmt.Sprintf("unknown command group %q; run \"mbx commands\" to list available groups", resource),
			}
			writeErr(stderr, llm, err)
			return cliErrors.ExitCodeFor(err)
		}
	} else {
		counts := make(map[string]int)
		for _, op := range ops {
			counts[op.Resource]++
		}
		resources := make([]string, 0, len(counts))
		for name := range counts {
			resources = append(resources, name)
		}
		sort.Strings(resources)
		for _, name := range resources {
			plural := "s"
			if counts[name] == 1 {
				plural = ""
			}
			cmds = append(cmds, cmdSummary{
				Name: name,
				Summary: fmt.Sprintf(
					"%d available command%s; run \"mbx commands %s\"",
					counts[name], plural, name,
				),
			})
		}
	}

	if llm {
		render.CompactJSON(stdout, struct {
			Commands []cmdSummary `json:"commands"`
		}{Commands: cmds})
		return 0
	}

	for _, command := range cmds {
		fmt.Fprintf(stdout, "%s\t%s\n", command.Name, command.Summary)
		if searchSet {
			fmt.Fprintf(stdout, "\trequired: %s\toptional: %s\n", listOrNone(command.Required), listOrNone(command.Optional))
		}
	}
	if searchSet && len(cmds) == 0 {
		fmt.Fprintln(stdout, "no matching operation; try other words, or run \"mbx commands\"")
	}
	return 0
}

func listOrNone(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ", ")
}

func runDescribe(stdout, stderr io.Writer, args []string) int {
	llm := false
	full := false
	var name string
	for _, a := range args {
		switch {
		case a == "--llm":
			llm = true
		case a == "--full":
			full = true
		case strings.HasPrefix(a, "--"):
			err := &cliErrors.Error{Code: "invalid_argument", Message: "unknown flag: " + a}
			writeErr(stderr, llm, err)
			return cliErrors.ExitCodeFor(err)
		case name == "":
			name = a
		default:
			err := &cliErrors.Error{Code: "invalid_argument", Message: "unexpected argument: " + a}
			writeErr(stderr, llm, err)
			return cliErrors.ExitCodeFor(err)
		}
	}

	if name == "" {
		err := &cliErrors.Error{Code: "invalid_argument", Message: "describe requires a command name"}
		writeErr(stderr, llm, err)
		return cliErrors.ExitCodeFor(err)
	}

	op, ok := operations.Default.Get(name)
	if !ok {
		err := &cliErrors.Error{Code: "invalid_argument", Message: "unknown command: " + name}
		writeErr(stderr, llm, err)
		return cliErrors.ExitCodeFor(err)
	}

	if llm {
		type argDesc struct {
			Type        string                `json:"type"`
			Required    bool                  `json:"required"`
			Effect      operations.SideEffect `json:"effect,omitempty"`
			Description string                `json:"description,omitempty"`
		}
		type outputDesc struct {
			Type         string   `json:"type"`
			Fields       []string `json:"fields"`
			DryRunFields []string `json:"dry_run_fields,omitempty"`
		}
		var argDescs map[string]argDesc
		if len(op.Args) > 0 {
			argDescs = make(map[string]argDesc, len(op.Args))
			for name, arg := range op.Args {
				desc := argDesc{Type: arg.Type, Required: arg.Required, Effect: arg.SideEffect}
				if full {
					desc.Description = arg.Description
				}
				argDescs[name] = desc
			}
		}

		result := struct {
			Name        string                `json:"name"`
			Summary     string                `json:"summary"`
			Usage       string                `json:"usage"`
			Effect      operations.SideEffect `json:"effect"`
			Idempotent  bool                  `json:"idempotent"`
			Args        map[string]argDesc    `json:"args,omitempty"`
			Output      outputDesc            `json:"output"`
			Method      string                `json:"method,omitempty"`
			Endpoint    string                `json:"endpoint,omitempty"`
			Action      string                `json:"action,omitempty"`
			Destructive *bool                 `json:"destructive,omitempty"`
			Scopes      *[]string             `json:"scopes,omitempty"`
		}{
			Name:       op.Name,
			Summary:    op.Summary,
			Usage:      usageFor(op),
			Effect:     op.SideEffect,
			Idempotent: op.Idempotent,
			Args:       argDescs,
			Output:     outputDesc{Type: op.Output.Type, Fields: op.Output.Fields, DryRunFields: op.Output.DryRunFields},
		}
		if full {
			result.Method = op.Method
			result.Endpoint = op.Endpoint
			result.Action = op.Action
			destructive := op.Destructive
			scopes := append([]string{}, op.Scopes...)
			result.Destructive = &destructive
			result.Scopes = &scopes
		}
		render.CompactJSON(stdout, result)
		return 0
	}

	writeOperationHelp(stdout, op)
	return 0
}

func toCLIError(err error, fallbackCode string) *cliErrors.Error {
	var cerr *cliErrors.Error
	if errors.As(err, &cerr) {
		return cerr
	}
	// A request that got no response, such as a timeout or a broken connection, is worth
	// trying again. The message leaves out the request URL, which holds the session.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return &cliErrors.Error{Code: "network_error", Message: "the request to mailbox.org got no response: " + urlErr.Err.Error(), Retryable: true}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &cliErrors.Error{Code: "network_error", Message: "the request to mailbox.org timed out", Retryable: true}
	}
	return &cliErrors.Error{Code: fallbackCode, Message: err.Error()}
}

func writeErr(w io.Writer, llm bool, err *cliErrors.Error) {
	_, forceStructured := w.(interface{ structuredErrors() })
	_ = render.Error(w, err, llm || forceStructured)
}

// writeWarning reports a non-fatal problem on stderr: {"warning":{"code","message"}} in
// structured modes, "Warning: <message>" otherwise. The command still succeeds.
func writeWarning(w io.Writer, llm bool, code, message string) {
	_, forceStructured := w.(interface{ structuredErrors() })
	if llm || forceStructured {
		_ = render.CompactJSON(w, struct {
			Warning struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"warning"`
		}{Warning: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: message}})
		return
	}
	fmt.Fprintf(w, "Warning: %s\n", message)
}

func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}
