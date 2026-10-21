// ABOUTME: Unit tests for the registry-driven flag parser and the pre-dispatch flag readers.
// ABOUTME: Pins its error messages and order, and checks that parser and readers agree.
package cli

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
)

func flagsTestOperation() operations.Operation {
	return operations.Operation{
		Name: "widgets.update",
		Args: map[string]operations.ArgSpec{
			"id":      {Type: "string", Required: true},
			"to":      {Type: "string"},
			"limit":   {Type: "int"},
			"when":    {Type: "string"},
			"dry-run": {Type: "bool"},
			"verify":  {Type: "boolean"},
		},
	}
}

func TestParseOperationFlags_ReadsValuesBoolsAndLLM(t *testing.T) {
	f, err := parseOperationFlags(flagsTestOperation(), []string{
		"--llm", "--id", "7", "--to", "a", "--dry-run", "--to", "b", "--id", "8",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !f.LLM() || !f.Bool("dry-run") || f.Bool("verify") {
		t.Errorf("LLM=%v dry-run=%v verify=%v, want true true false", f.LLM(), f.Bool("dry-run"), f.Bool("verify"))
	}
	if got := f.String("id"); got != "8" {
		t.Errorf("String(id) = %q, want the last value 8", got)
	}
	if got := f.Values("to"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Values(to) = %q, want [a b]", got)
	}
	if value, given := f.Lookup("when"); given || value != "" {
		t.Errorf("Lookup(when) = %q, %v, want absent", value, given)
	}
	if got := f.Values("when"); got != nil {
		t.Errorf("Values(when) = %q, want nil", got)
	}
}

func TestParseOperationFlags_EmptyValueIsGiven(t *testing.T) {
	f, err := parseOperationFlags(flagsTestOperation(), []string{"--when", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if value, given := f.Lookup("when"); !given || value != "" {
		t.Errorf("Lookup(when) = %q, %v, want given and empty", value, given)
	}
}

func TestParseOperationFlags_ValueMayLookLikeAFlag(t *testing.T) {
	f, err := parseOperationFlags(flagsTestOperation(), []string{"--id", "--dry-run", "--to", "--bogus"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.String("id") != "--dry-run" || f.String("to") != "--bogus" || f.Bool("dry-run") {
		t.Errorf("id=%q to=%q dry-run=%v, want the flag-shaped values consumed", f.String("id"), f.String("to"), f.Bool("dry-run"))
	}
}

func TestParseOperationFlags_Errors(t *testing.T) {
	positive := intFlag("limit", 1, math.MaxInt, "--limit must be a positive integer")
	notNow := checkedFlag("when", func(value string) *cliErrors.Error {
		if value == "now" {
			return invalidArgument("--when must not be now")
		}
		return nil
	})
	cases := []struct {
		name    string
		args    []string
		message string
		llm     bool
	}{
		{"unknown flag", []string{"--bogus"}, "unknown flag: --bogus", false},
		{"positional argument", []string{"stray"}, "unknown flag: stray", false},
		{"equals form", []string{"--id=7"}, "unknown flag: --id=7", false},
		{"single dash", []string{"-h"}, "unknown flag: -h", false},
		{"double dash alone", []string{"--"}, "unknown flag: --", false},
		{"missing value", []string{"--llm", "--id"}, "--id requires a value", true},
		{"int not a number", []string{"--limit", "x"}, "--limit must be a positive integer", false},
		{"int below range", []string{"--limit", "0"}, "--limit must be a positive integer", false},
		{"checked value", []string{"--when", "now"}, "--when must not be now", false},
		{"first error wins: rule", []string{"--limit", "0", "--bogus"}, "--limit must be a positive integer", false},
		{"first error wins: unknown", []string{"--bogus", "--limit", "0"}, "unknown flag: --bogus", false},
		{"llm after the error", []string{"--bogus", "--llm"}, "unknown flag: --bogus", false},
		{"llm before the error", []string{"--llm", "--bogus"}, "unknown flag: --bogus", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseOperationFlags(flagsTestOperation(), tc.args, positive, notNow)
			if err == nil {
				t.Fatalf("args %q parsed without error", tc.args)
			}
			if err.Code != "invalid_argument" || err.Message != tc.message {
				t.Errorf("error = %s %q, want invalid_argument %q", err.Code, err.Message, tc.message)
			}
			if cliErrors.ExitCodeFor(err) != 4 {
				t.Errorf("exit code = %d, want 4", cliErrors.ExitCodeFor(err))
			}
			if f.LLM() != tc.llm {
				t.Errorf("LLM() = %v, want %v", f.LLM(), tc.llm)
			}
		})
	}
}

func TestParseOperationFlags_IntRule(t *testing.T) {
	rule := intFlag("limit", 1, 5, "--limit must be an integer from 1 to 5")
	f, err := parseOperationFlags(flagsTestOperation(), []string{"--limit", "2", "--limit", "5"}, rule)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n, given := f.Int("limit"); !given || n != 5 {
		t.Errorf("Int(limit) = %d, %v, want 5 true", n, given)
	}
	if _, err := parseOperationFlags(flagsTestOperation(), []string{"--limit", "6"}, rule); err == nil {
		t.Error("--limit 6 parsed, want the range error")
	}
	f, _ = parseOperationFlags(flagsTestOperation(), nil, rule)
	if n, given := f.Int("limit"); given || n != 0 {
		t.Errorf("Int(limit) = %d, %v without the flag, want 0 false", n, given)
	}
}

func TestParseOperationFlags_OnlyLLMForOperationWithoutArgs(t *testing.T) {
	op := operations.Operation{Name: "widgets.list"}
	if _, err := parseOperationFlags(op, []string{"--llm"}); err != nil {
		t.Errorf("--llm rejected: %v", err)
	}
	if _, err := parseOperationFlags(op, []string{"--dry-run"}); err == nil || err.Message != "unknown flag: --dry-run" {
		t.Errorf("error = %v, want unknown flag: --dry-run", err)
	}
}

func TestCommandFlags_MisuseIsAProgrammingError(t *testing.T) {
	f, err := parseOperationFlags(flagsTestOperation(), []string{"--limit", "3"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cases := map[string]func(){
		"unregistered name":       func() { f.String("nope") },
		"String on a bool flag":   func() { f.String("dry-run") },
		"Bool on a value flag":    func() { f.Bool("id") },
		"Int without an int rule": func() { f.Int("limit") },
		"rule for a bool flag": func() {
			parseOperationFlags(flagsTestOperation(), nil, intFlag("dry-run", 0, 1, "x"))
		},
		"rule for an unregistered flag": func() {
			parseOperationFlags(flagsTestOperation(), nil, intFlag("nope", 0, 1, "x"))
		},
		"unregistered operation": func() { parseCommandFlags("no.such-operation", nil) },
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("no panic")
				}
			}()
			call()
		})
	}
	if !f.Accepts("id") || f.Accepts("nope") {
		t.Errorf("Accepts(id)=%v Accepts(nope)=%v, want true false", f.Accepts("id"), f.Accepts("nope"))
	}
}

// TestFlagReaders_AgreeWithParser checks that the pre-dispatch readers (delete protection,
// --select checks, dry-run detection) see the same values as the handler parser for every
// argument list that the parser accepts.
func TestFlagReaders_AgreeWithParser(t *testing.T) {
	op := flagsTestOperation()
	argLists := [][]string{
		{"--id", "1", "--to", "Trash"},
		{"--id", "--to", "--to", "Inbox"},
		{"--to", "--dry-run"},
		{"--to", "--verify", "--verify"},
		{"--to", "a", "--to", "b", "--dry-run"},
		{"--llm", "--when", "--id", "--id", "x"},
		{"--id", "--preflight"},
	}
	for _, args := range argLists {
		f, err := parseOperationFlags(op, args)
		if err != nil {
			t.Fatalf("args %q: %v", args, err)
		}
		withName := append([]string{"widgets", "update"}, args...)
		for _, name := range []string{"id", "to", "when"} {
			gotValue, gotFound := commandStringFlagValue(withName, op, 2, name)
			wantValue, wantFound := f.Lookup(name)
			if gotValue != wantValue || gotFound != wantFound {
				t.Errorf("args %q --%s: reader = %q %v, parser = %q %v", args, name, gotValue, gotFound, wantValue, wantFound)
			}
		}
		for _, name := range []string{"dry-run", "verify"} {
			if got, want := commandBoolFlagPresent(withName, op, 2, name), f.Bool(name); got != want {
				t.Errorf("args %q --%s: reader = %v, parser = %v", args, name, got, want)
			}
		}
		if got, want := commandRequestsDryRun(withName, op, 2), f.Bool("dry-run"); got != want {
			t.Errorf("args %q: commandRequestsDryRun = %v, parser dry-run = %v", args, got, want)
		}
	}
}

func TestCommandRequestsDryRun_MatchesUnregisteredSpellings(t *testing.T) {
	op := operations.Operation{Name: "widgets.delete", Args: map[string]operations.ArgSpec{"id": {Type: "string"}}}
	if !commandRequestsDryRun([]string{"widgets", "delete", "--preflight"}, op, 2) {
		t.Error("--preflight not detected")
	}
	if commandRequestsDryRun([]string{"widgets", "delete", "--id", "--dry-run"}, op, 2) {
		t.Error("--dry-run given as the --id value was taken for a dry run")
	}
}

func TestHelpRequest_IgnoresHelpGivenAsFlagValue(t *testing.T) {
	if target, ok := helpRequest([]string{"snippet", "get", "--id", "--help"}); ok {
		t.Errorf("helpRequest = %q, true; want no help request when --help is the --id value", target)
	}
	target, ok := helpRequest([]string{"snippet", "get", "--id", "2", "--help"})
	if !ok || !reflect.DeepEqual(target, []string{"snippet.get"}) {
		t.Errorf("helpRequest = %q, %v; want [snippet.get] true", target, ok)
	}
	target, ok = helpRequest([]string{"nosuch", "--x", "-h"})
	if !ok || !reflect.DeepEqual(target, []string{"nosuch"}) {
		t.Errorf("helpRequest = %q, %v; want [nosuch] true for an unknown command", target, ok)
	}
}

func TestExitWithError_WritesOnceAndReturnsExitCode(t *testing.T) {
	var stderr bytes.Buffer
	code := exitWithError(&stderr, true, invalidArgument("bad"))
	if code != 4 {
		t.Errorf("exit code = %d, want 4", code)
	}
	if got := stderr.String(); strings.Count(got, `"error"`) != 1 || !strings.Contains(got, `"invalid_argument"`) {
		t.Errorf("stderr = %q, want one invalid_argument error document", got)
	}
}
