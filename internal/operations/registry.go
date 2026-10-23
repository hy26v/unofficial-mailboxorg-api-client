// ABOUTME: Typed operation registry: canonical metadata for each implemented API
// ABOUTME: operation, used to drive CLI parsing, help, discovery, and contract tests.
package operations

import (
	"sort"
	"strings"
)

// SideEffect classifies the mutation impact of an operation.
type SideEffect string

const (
	SideEffectRead        SideEffect = "read"
	SideEffectWrite       SideEffect = "write"
	SideEffectDestructive SideEffect = "destructive"
)

// ArgSpec describes one input argument (flag) of an operation.
type ArgSpec struct {
	Name        string
	Type        string
	Required    bool
	Description string
	// SideEffect is set when giving this flag changes account data although the operation
	// itself is a read, for example mail read --mark-seen. Empty means the flag adds no
	// side effect of its own.
	SideEffect SideEffect
}

// TakesValue reports whether the flag uses the next argument as its value. Only "bool"
// and "boolean" flags are given bare (for example --dry-run). The CLI flag parser, help
// and the pre-dispatch safety checks all use this one rule, so they read an argument
// list the same way.
func (a ArgSpec) TakesValue() bool {
	return a.Type != "bool" && a.Type != "boolean"
}

// OutputSpec describes the shape of an operation's successful output.
type OutputSpec struct {
	Type   string
	Fields []string
	// ScalarFields lists the Fields that always hold one JSON scalar (string, number,
	// boolean or null). A dotted --select path into one of them can never match, so the
	// CLI rejects it before the command runs.
	ScalarFields []string
	// DryRunFields lists the top-level fields of the --dry-run result, when the operation
	// has a dry run. --select with --dry-run is checked against this list.
	DryRunFields []string
	// OnRequestFields lists the Fields that the result holds only when the comma-separated
	// OnRequestArg names them, for example mail list --fields to. A --select of such a
	// field adds it to OnRequestArg before the command runs.
	OnRequestFields []string
	OnRequestArg    string
}

// Operation is the canonical metadata for one implemented API operation.
type Operation struct {
	Name        string // canonical name, e.g. "whoami" or "users.get"
	Resource    string
	Action      string
	Summary     string
	Method      string // HTTP method
	Endpoint    string // path relative to the base URL, e.g. "/user"
	SideEffect  SideEffect
	Destructive bool
	Idempotent  bool
	Scopes      []string
	// Keywords are extra search words, such as synonyms an agent may use ("alias" for
	// mail.senders). Search also matches the name, summary and arguments.
	Keywords []string
	Args     map[string]ArgSpec
	Output   OutputSpec
}

// Registry holds the operations implemented by this CLI, keyed by canonical name.
type Registry struct {
	ops map[string]Operation
}

// NewRegistry returns an empty operation registry.
func NewRegistry() *Registry {
	return &Registry{ops: make(map[string]Operation)}
}

func cloneOperation(op Operation) Operation {
	op.Scopes = append([]string(nil), op.Scopes...)
	op.Keywords = append([]string(nil), op.Keywords...)
	if op.Args != nil {
		args := make(map[string]ArgSpec, len(op.Args))
		for name, spec := range op.Args {
			args[name] = spec
		}
		op.Args = args
	}
	op.Output.Fields = append([]string(nil), op.Output.Fields...)
	op.Output.ScalarFields = append([]string(nil), op.Output.ScalarFields...)
	op.Output.DryRunFields = append([]string(nil), op.Output.DryRunFields...)
	return op
}

// Register adds an operation to the registry.
func (r *Registry) Register(op Operation) {
	r.ops[op.Name] = cloneOperation(op)
}

// Get looks up an operation by canonical name.
func (r *Registry) Get(name string) (Operation, bool) {
	op, ok := r.ops[name]
	if !ok {
		return Operation{}, false
	}
	return cloneOperation(op), true
}

// List returns all registered operations, sorted by name for deterministic output.
func (r *Registry) List() []Operation {
	names := make([]string, 0, len(r.ops))
	for name := range r.ops {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]Operation, len(names))
	for i, name := range names {
		out[i] = cloneOperation(r.ops[name])
	}
	return out
}

// Default is the process-wide registry of implemented, tested operations. Each resource's
// own file registers its operations into Default from an init() function, so adding a
// resource never requires editing a shared registry-building function.
var Default = NewRegistry()

// Register adds an operation to the default registry.
func Register(op Operation) {
	Default.Register(op)
}

// Search ranks the operations for a free-text query. Every query word must match the
// name, a keyword, the summary or an argument; a name or keyword match ranks above a
// summary match, which ranks above an argument match. Operations that keep returns false
// for are left out. It returns at most limit operations, best first.
func (r *Registry) Search(query string, limit int, keep func(Operation) bool) []Operation {
	words := searchWords(query)
	if len(words) == 0 {
		return nil
	}
	type scored struct {
		op    Operation
		score int
	}
	var matches []scored
	for _, op := range r.List() {
		if keep != nil && !keep(op) {
			continue
		}
		total := 0
		for _, word := range words {
			best := wordScore(op, word)
			if best == 0 {
				total = 0
				break
			}
			total += best
		}
		if total > 0 {
			matches = append(matches, scored{op, total})
		}
	}
	// Equal scores list reads first, because most tasks start with one.
	effectRank := map[SideEffect]int{SideEffectRead: 0, SideEffectWrite: 1, SideEffectDestructive: 2}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		return effectRank[matches[i].op.SideEffect] < effectRank[matches[j].op.SideEffect]
	})
	var out []Operation
	for i := 0; i < len(matches) && (limit <= 0 || i < limit); i++ {
		out = append(out, matches[i].op)
	}
	return out
}

// SearchSome ranks the operations that match at least one query word, for a query that no
// operation matches in full: the most matched words first, then the best score. Words of
// two letters or fewer, such as "in" or "of", do not count.
func (r *Registry) SearchSome(query string, limit int, keep func(Operation) bool) []Operation {
	var words []string
	for _, word := range searchWords(query) {
		if len(word) > 2 {
			words = append(words, word)
		}
	}
	type scored struct {
		op             Operation
		matched, score int
	}
	var matches []scored
	for _, op := range r.List() {
		if keep != nil && !keep(op) {
			continue
		}
		entry := scored{op: op}
		for _, word := range words {
			if score := wordScore(op, word); score > 0 {
				entry.matched++
				entry.score += score
			}
		}
		if entry.matched > 0 {
			matches = append(matches, entry)
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].matched != matches[j].matched {
			return matches[i].matched > matches[j].matched
		}
		return matches[i].score > matches[j].score
	})
	var out []Operation
	for i := 0; i < len(matches) && (limit <= 0 || i < limit); i++ {
		out = append(out, matches[i].op)
	}
	return out
}

func searchWords(query string) []string {
	var words []string
	for _, word := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '@')
	}) {
		words = append(words, word)
	}
	return words
}

// wordScore is the best score of word or its singular forms, so "senders" finds
// "sender" and "aliases" finds "alias".
func wordScore(op Operation, word string) int {
	best := fieldScore(op, word)
	if len(word) > 3 && strings.HasSuffix(word, "s") && !strings.HasSuffix(word, "ss") {
		best = max(best, fieldScore(op, strings.TrimSuffix(word, "s")))
		if strings.HasSuffix(word, "es") {
			best = max(best, fieldScore(op, strings.TrimSuffix(word, "es")))
		}
	}
	return best
}

func fieldScore(op Operation, word string) int {
	name := strings.ToLower(op.Name)
	switch {
	case name == word || op.Resource == word || op.Action == word:
		return 8
	case strings.Contains(name, word):
		return 6
	}
	for _, keyword := range op.Keywords {
		if strings.Contains(strings.ToLower(keyword), word) {
			return 6
		}
	}
	if strings.Contains(strings.ToLower(op.Summary), word) {
		return 3
	}
	for argName, spec := range op.Args {
		if strings.Contains(argName, word) {
			return 2
		}
		if strings.Contains(strings.ToLower(spec.Description), word) {
			return 1
		}
	}
	return 0
}

// RequiredAndOptionalArgs splits the argument names of op, each list sorted. Names for
// which skip returns true are left out.
func (op Operation) RequiredAndOptionalArgs(skip func(name string) bool) (required, optional []string) {
	names := make([]string, 0, len(op.Args))
	for name := range op.Args {
		if skip == nil || !skip(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if op.Args[name].Required {
			required = append(required, name)
		} else {
			optional = append(optional, name)
		}
	}
	return required, optional
}
