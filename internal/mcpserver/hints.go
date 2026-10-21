// ABOUTME: Teaching error results for MCP tool calls: argument lists, examples and name suggestions.
// ABOUTME: Lets an agent correct a failed call at once instead of calling mbx_describe first.
package mcpserver

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// argHint is the second content block of an argument error: what the called tool accepts
// and one call that has every required argument.
type argHint struct {
	Operation string            `json:"operation"`
	Tool      string            `json:"tool"`
	Required  []string          `json:"required,omitempty"`
	Args      map[string]string `json:"args"`
	Output    []string          `json:"output"`
	Example   map[string]any    `json:"example"`
	Note      string            `json:"note,omitempty"`
}

// hintFor describes the arguments of op that MCP offers. With a task tool, it describes
// only the arguments of that tool, its example has no operation name, and the note names
// the arguments that only the run tool takes.
func (h *handlers) hintFor(op operations.Operation, task *taskTool) argHint {
	hint := argHint{Operation: op.Name, Tool: h.toolFor(op), Args: map[string]string{}, Output: op.Output.Fields}
	offered := func(string) bool { return true }
	if task != nil {
		hint.Tool = task.Name
		names := map[string]bool{}
		for _, name := range task.Args {
			names[name] = true
		}
		offered = func(name string) bool { return names[name] }
	}
	example := map[string]any{}
	var others []string
	for name, spec := range op.Args {
		if hiddenArg(name) {
			continue
		}
		if !offered(name) {
			others = append(others, name)
			continue
		}
		text := spec.Type
		if spec.Required {
			text += ", required"
			hint.Required = append(hint.Required, name)
			example[name] = examplePlaceholder(name, spec)
		}
		if spec.Description != "" {
			text += ": " + mcpText(spec.Description)
		}
		hint.Args[name] = text
	}
	sort.Strings(hint.Required)
	if task != nil {
		hint.Example = example
		if len(others) > 0 {
			sort.Strings(others)
			hint.Note = fmt.Sprintf("%s with operation %s also takes %s", h.toolFor(op), op.Name, strings.Join(others, ", "))
		}
		return hint
	}
	hint.Example = map[string]any{"operation": op.Name}
	if len(example) > 0 {
		hint.Example["args"] = example
	}
	return hint
}

// cliOnly matches a parenthetical that mentions a flag MCP does not offer, or the CLI's
// --format, for example "(use --format jsonl or --output for large results)".
var cliOnly = func() *regexp.Regexp {
	names := []string{"format"}
	for name := range blockedArgs {
		names = append(names, name)
	}
	for name := range serverArgs {
		names = append(names, name)
	}
	sort.Strings(names)
	return regexp.MustCompile(`\s*\([^()]*--(?:` + strings.Join(names, "|") + `)\b[^()]*\)`)
}()

var flagName = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)

// mcpText rewrites a registry description in tool-argument terms: it drops remarks about
// CLI-only flags and writes "--recursive" as "recursive".
func mcpText(text string) string {
	return flagName.ReplaceAllString(cliOnly.ReplaceAllString(text, ""), "$1")
}

func examplePlaceholder(name string, spec operations.ArgSpec) any {
	switch spec.Type {
	case "int", "integer":
		return 1
	case "bool", "boolean":
		return true
	}
	return "<" + name + ">"
}

// argumentError is an invalid_argument result with the hint for op, as task (nil for a
// run tool) offers it.
func (h *handlers) argumentError(op operations.Operation, task *taskTool, message string) *mcp.CallToolResult {
	result := toolError("invalid_argument", message)
	return h.withHint(result, op, task)
}

func (h *handlers) withHint(result *mcp.CallToolResult, op operations.Operation, task *taskTool) *mcp.CallToolResult {
	data, err := json.Marshal(map[string]any{"hint": h.hintFor(op, task)})
	if err == nil {
		result.Content = append(result.Content, &mcp.TextContent{Text: string(data)})
	}
	return result
}

// unknownArgMessage names the closest argument names and lists all of them.
func unknownArgMessage(op operations.Operation, name string) string {
	var names []string
	for arg := range op.Args {
		if !hiddenArg(arg) {
			names = append(names, arg)
		}
	}
	sort.Strings(names)
	message := fmt.Sprintf("%s has no argument %q", op.Name, name)
	if len(names) == 0 {
		return message + "; it takes no arguments"
	}
	if close := closestNames(name, names); len(close) > 0 {
		message += fmt.Sprintf("; did you mean %s?", quoteList(close))
	} else {
		message += ";"
	}
	return message + " the hint lists its arguments"
}

// unknownOperationMessage suggests registered operations close to name.
func (h *handlers) unknownOperationMessage(name string) string {
	var names []string
	for _, op := range operations.Default.List() {
		if h.offered(op) {
			names = append(names, op.Name)
		}
	}
	message := fmt.Sprintf("unknown operation %q", name)
	normalized := strings.NewReplacer("_", ".", " ", ".", "/", ".").Replace(strings.ToLower(strings.TrimSpace(name)))
	if close := closestNames(normalized, names); len(close) > 0 {
		return message + fmt.Sprintf("; did you mean %s?", quoteList(close))
	}
	return message + "; call mbx_commands with a query, for example {\"query\":\"send mail\"}, to search the operations"
}

// closestNames returns up to three candidates: an exact match first, then those that
// contain name or are contained in it, then those a small edit distance away.
func closestNames(name string, candidates []string) []string {
	name = strings.ToLower(name)
	type scored struct {
		name  string
		score int
	}
	var matches []scored
	limit := 2
	if len(name) > 8 {
		limit = len(name) / 4
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(candidate)
		switch {
		case lower == name:
			matches = append(matches, scored{candidate, -1})
		case strings.Contains(lower, name) || (len(lower) > 2 && strings.Contains(name, lower)):
			matches = append(matches, scored{candidate, 0})
		default:
			if d := editDistance(name, lower); d <= limit {
				matches = append(matches, scored{candidate, d})
			}
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score < matches[j].score
		}
		return matches[i].name < matches[j].name
	})
	var out []string
	for i := 0; i < len(matches) && i < 3; i++ {
		out = append(out, matches[i].name)
	}
	return out
}

func editDistance(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func quoteList(names []string) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = fmt.Sprintf("%q", name)
	}
	return strings.Join(quoted, " or ")
}
