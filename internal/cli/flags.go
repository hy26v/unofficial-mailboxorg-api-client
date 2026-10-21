// ABOUTME: The one registry-driven flag parser for command handlers and pre-dispatch checks.
// ABOUTME: Reads flags from operation ArgSpecs so handlers, help and safety checks agree.
package cli

// Flag parsing guide for command handlers
//
// Every command reads its flags with parseCommandFlags. The operation's registry entry
// (operations.Default) is the only list of accepted flags: a flag the registry does not
// declare for that operation is rejected, and help, describe, delete protection, the
// --select checks and the handler all read the argument list the same way through
// scanOperationArgs.
//
// Declare flags in the operation's init() registration:
//
//	Args: map[string]operations.ArgSpec{
//		"id":      {Type: "string", Required: true, Description: "..."},
//		"limit":   {Type: "int", Description: "..."},
//		"dry-run": {Type: "bool", Description: "..."},
//	}
//
// Type "bool" or "boolean" is a bare flag (--dry-run). Every other type uses the next
// argument as its value, also when that argument starts with "--" (--id --to gives id
// the value "--to"). "--name=value" is not supported; it is an unknown flag.
//
// Read flags in the handler:
//
//	f, ferr := parseCommandFlags("snippet.get", args,
//		intFlag("limit", 1, math.MaxInt, "--limit must be a positive integer"))
//	if ferr != nil {
//		return exitWithError(stderr, f.LLM(), ferr)
//	}
//	llm := f.LLM()
//	id := f.String("id")             // last value, or "" when absent
//	body, given := f.Lookup("body")  // tells "absent" from "given as empty"
//	to := f.Values("to")             // every value in order, for a repeatable flag
//	limit, ok := f.Int("limit")      // only for flags declared with intFlag
//	dryRun := f.Bool("dry-run")
//
// The parser produces these errors, all with code invalid_argument (exit 4):
//
//	"unknown flag: <arg>"         for an undeclared flag, a positional argument or --x=y
//	"--<name> requires a value"   when a value flag is the last argument
//	the rule's own message         when an intFlag or checkedFlag rule rejects a value
//
// It stops at the first error, in argument order, like the hand-written loops did. f.LLM()
// is true only when --llm came before the failing argument, which is what the old loops
// passed to writeErr. Use it for the error output.
//
// Conventions and pitfalls found while migrating:
//
//   - Value checks that the old loop did inside the loop (integer ranges, RFC 3339 dates,
//     "must not be empty") must become intFlag or checkedFlag rules. Checks that the old
//     code did after the loop (required flags, --confirm must repeat --id, "at least one
//     of") stay after parseCommandFlags, in the same order. Otherwise a different error
//     wins when an argument list has two problems, and tests that compare messages fail.
//   - The parser does not enforce ArgSpec.Required. Handlers keep their own required-flag
//     messages, for example "--id is required, e.g. --id 2".
//   - A repeated value flag keeps every value; String, Lookup and Int return the last
//     one, as the old loops did. Use Values for repeatable flags such as mail --to.
//   - Accessors panic for a name that the operation does not register. This finds typos
//     on the first test run. A helper that several operations share can call
//     f.Accepts(name) before it reads a flag that only some of them register.
//   - If a handler accepts a flag that its registry entry does not declare, adding the
//     ArgSpec changes describe and help output. Report that to the coordinator; do not
//     add or drop flags silently. TestDefault_AcceptedDryRunIsRegistered and
//     TestDefault_BoolArgsAreValuelessFlags check part of this contract.
//   - Global output flags (--format, --select, the user's --llm) are removed by
//     parseOutputOptions before dispatch. Handlers see only the internal --llm marker,
//     which the parser accepts for every operation.
//   - Handlers with positional arguments, or with flags that change meaning by mode,
//     need their own code for that part. Keep it small and keep the same messages.
//   - Resource dispatchers (runSnippet and similar) keep hasFlag(args, "--llm") for the
//     "unknown <resource> action" error.

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
)

// invalidArgument is the error for a bad command line (code invalid_argument, exit 4).
func invalidArgument(message string) *cliErrors.Error {
	return &cliErrors.Error{Code: "invalid_argument", Message: message}
}

// exitWithError writes err to stderr in the requested format and returns its exit code.
func exitWithError(stderr io.Writer, llm bool, err *cliErrors.Error) int {
	writeErr(stderr, llm, err)
	return cliErrors.ExitCodeFor(err)
}

// flagToken is one argument as scanOperationArgs reads it. Value is set only for a
// registered value flag that has a following argument; that argument is then consumed and
// is never visited as a token of its own.
type flagToken struct {
	index    int
	arg      string
	name     string // the flag name without "--"; set only when known is true
	spec     operations.ArgSpec
	known    bool // the operation registers this flag
	value    string
	hasValue bool
}

// scanOperationArgs visits args[start:] the way every command handler reads them. A token
// "--name" (without "=") that the operation registers with a value type consumes the next
// argument as its value, whatever that argument looks like. All other tokens, including
// unregistered flags, bool flags and "--x=y", consume nothing. visit returns true to stop.
//
// This is the only place that decides which argument is a value. The handler parser and
// the pre-dispatch checks (help, dry-run detection, delete protection, --select) all use
// it, so they cannot read the same argument list in different ways.
func scanOperationArgs(args []string, op operations.Operation, start int, visit func(flagToken) bool) {
	for i := start; i < len(args); i++ {
		token := flagToken{index: i, arg: args[i]}
		if strings.HasPrefix(token.arg, "--") && !strings.Contains(token.arg, "=") {
			name := strings.TrimPrefix(token.arg, "--")
			if spec, ok := op.Args[name]; ok {
				token.name, token.spec, token.known = name, spec, true
				if spec.TakesValue() && i+1 < len(args) {
					i++
					token.value, token.hasValue = args[i], true
				}
			}
		}
		if visit(token) {
			return
		}
	}
}

// flagRule is a parse-time check for one value flag, applied to each value in argument
// order. Build rules with intFlag or checkedFlag.
type flagRule struct {
	name  string
	isInt bool
	check func(value string) (int, *cliErrors.Error)
}

// intFlag accepts only a base-10 integer from min to max (inclusive) for --name and
// rejects any other value with message. Read the value with commandFlags.Int.
func intFlag(name string, min, max int, message string) flagRule {
	return flagRule{name: name, isInt: true, check: func(value string) (int, *cliErrors.Error) {
		n, err := strconv.Atoi(value)
		if err != nil || n < min || n > max {
			return 0, invalidArgument(message)
		}
		return n, nil
	}}
}

// checkedFlag runs check on each value of --name while the arguments are parsed. A
// non-nil result stops the parse with that error.
func checkedFlag(name string, check func(value string) *cliErrors.Error) flagRule {
	return flagRule{name: name, check: func(value string) (int, *cliErrors.Error) {
		return 0, check(value)
	}}
}

// commandFlags is the parsed flag set of one registered operation.
type commandFlags struct {
	op     operations.Operation
	llm    bool
	values map[string][]string
	bools  map[string]bool
	ints   map[string]int
	isInt  map[string]bool
}

// parseCommandFlags parses a handler's arguments (everything after the action name)
// against the registry entry of operation. See the guide at the top of this file. The
// returned flags are usable also when the error is non-nil: LLM() then tells how to
// write that error.
func parseCommandFlags(operation string, args []string, rules ...flagRule) (commandFlags, *cliErrors.Error) {
	op, ok := operations.Default.Get(operation)
	if !ok {
		panic(fmt.Sprintf("parseCommandFlags: operation %q is not registered", operation))
	}
	return parseOperationFlags(op, args, rules...)
}

// parseOperationFlags is parseCommandFlags for an operation value. Tests use it with
// operations that are not in the default registry.
func parseOperationFlags(op operations.Operation, args []string, rules ...flagRule) (commandFlags, *cliErrors.Error) {
	f := commandFlags{
		op:     op,
		values: map[string][]string{},
		bools:  map[string]bool{},
		ints:   map[string]int{},
		isInt:  map[string]bool{},
	}
	ruleFor := make(map[string]flagRule, len(rules))
	for _, rule := range rules {
		spec, registered := op.Args[rule.name]
		if !registered || !spec.TakesValue() {
			panic(fmt.Sprintf("parseCommandFlags: %s has no value flag --%s for a rule", op.Name, rule.name))
		}
		ruleFor[rule.name] = rule
		if rule.isInt {
			f.isInt[rule.name] = true
		}
	}

	var parseErr *cliErrors.Error
	scanOperationArgs(args, op, 0, func(token flagToken) bool {
		switch {
		case token.arg == "--llm":
			f.llm = true
		case !token.known:
			parseErr = invalidArgument("unknown flag: " + token.arg)
		case !token.spec.TakesValue():
			f.bools[token.name] = true
		case !token.hasValue:
			parseErr = invalidArgument(token.arg + " requires a value")
		default:
			if rule, ok := ruleFor[token.name]; ok {
				n, err := rule.check(token.value)
				if err != nil {
					parseErr = err
					return true
				}
				if rule.isInt {
					f.ints[token.name] = n
				}
			}
			f.values[token.name] = append(f.values[token.name], token.value)
		}
		return parseErr != nil
	})
	return f, parseErr
}

// spec returns the registered ArgSpec for name and panics when the operation does not
// register it, so a misspelt flag name fails on the first test run.
func (f commandFlags) spec(name string) operations.ArgSpec {
	spec, ok := f.op.Args[name]
	if !ok {
		panic(fmt.Sprintf("commandFlags: %s does not register --%s", f.op.Name, name))
	}
	return spec
}

func (f commandFlags) valueSpec(name string) {
	if !f.spec(name).TakesValue() {
		panic(fmt.Sprintf("commandFlags: --%s of %s is a bool flag; read it with Bool", name, f.op.Name))
	}
}

// LLM reports whether the internal --llm marker was given (before the first error).
func (f commandFlags) LLM() bool { return f.llm }

// Accepts reports whether the operation registers --name. Shared helpers use it before
// they read a flag that only some of their operations accept.
func (f commandFlags) Accepts(name string) bool {
	_, ok := f.op.Args[name]
	return ok
}

// Bool reports whether the bool flag --name was given.
func (f commandFlags) Bool(name string) bool {
	if f.spec(name).TakesValue() {
		panic(fmt.Sprintf("commandFlags: --%s of %s takes a value; read it with String", name, f.op.Name))
	}
	return f.bools[name]
}

// Lookup returns the last value of --name and whether the flag was given at all.
func (f commandFlags) Lookup(name string) (string, bool) {
	f.valueSpec(name)
	values := f.values[name]
	if len(values) == 0 {
		return "", false
	}
	return values[len(values)-1], true
}

// String returns the last value of --name, or "" when it was not given.
func (f commandFlags) String(name string) string {
	value, _ := f.Lookup(name)
	return value
}

// Values returns every value of --name in argument order (nil when it was not given).
func (f commandFlags) Values(name string) []string {
	f.valueSpec(name)
	return append([]string(nil), f.values[name]...)
}

// Int returns the last value of --name, already checked by its intFlag rule, and
// whether the flag was given.
func (f commandFlags) Int(name string) (int, bool) {
	f.valueSpec(name)
	if !f.isInt[name] {
		panic(fmt.Sprintf("commandFlags: --%s of %s has no intFlag rule", name, f.op.Name))
	}
	n, ok := f.ints[name]
	return n, ok
}

// commandRequestsDryRun reports whether a registered command asks for --dry-run or
// --preflight, so the run sends no change. It also matches those spellings when the
// operation does not register them; the handler then rejects the unknown flag.
func commandRequestsDryRun(args []string, op operations.Operation, flagStart int) bool {
	found := false
	scanOperationArgs(args, op, flagStart, func(token flagToken) bool {
		found = token.arg == "--dry-run" || token.arg == "--preflight"
		return found
	})
	return found
}

// commandStringFlagValue returns the value the handler will read for the value flag
// --target. A flag-shaped value (for example, --id --to) is not taken for a separate
// flag. Repeated flags use the last value, matching commandFlags.String.
func commandStringFlagValue(args []string, op operations.Operation, flagStart int, target string) (string, bool) {
	var value string
	found := false
	scanOperationArgs(args, op, flagStart, func(token flagToken) bool {
		if token.known && token.hasValue && token.name == target {
			value, found = token.value, true
		}
		return false
	})
	return value, found
}

// commandWriteFlag returns the name of the first given flag that changes data by itself
// (ArgSpec.SideEffect is set and is not read), or "" when there is none. A flag name
// given as the value of another flag does not count.
func commandWriteFlag(args []string, op operations.Operation, flagStart int) string {
	name := ""
	scanOperationArgs(args, op, flagStart, func(token flagToken) bool {
		if token.known && token.spec.SideEffect != "" && token.spec.SideEffect != operations.SideEffectRead {
			name = token.name
		}
		return name != ""
	})
	return name
}

// commandBoolFlagPresent reports whether the bool flag --target is given. A value such as
// "--verify" given to a preceding value flag is not taken for the bool flag.
func commandBoolFlagPresent(args []string, op operations.Operation, flagStart int, target string) bool {
	found := false
	scanOperationArgs(args, op, flagStart, func(token flagToken) bool {
		found = token.known && !token.spec.TakesValue() && token.name == target
		return found
	})
	return found
}
