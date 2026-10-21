// ABOUTME: Measures how many MCP tool calls an agent needs for common mbx tasks, against a fake mailbox.
// ABOUTME: "serve" runs the real mbx MCP server over stdio; "run" drives claude -p per task and checks it.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/cli"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const usage = `usage:
  go run ./scripts/mcp-eval run [--tasks FILE] [--variants task-tools,generic] [--model M] [--repeat N] [--out FILE]
      run every task with "claude -p" for each server variant and print the calls per task
  go run ./scripts/mcp-eval serve [--log FILE] [--task-tools=false]
      serve the mbx MCP server over stdio against the fake mailbox (claude starts this)
`

// evalWrites mirrors a cautious owner policy: drafts and moves, no sending, no deletes.
var evalWrites = map[string]bool{
	"mail.draft": true, "mail.move": true, "mail.copy": true, "mail.flag": true, "mail.read": true,
	"contacts.create": true, "contacts.update": true, "events.create": true, "events.update": true,
	"tasks.create": true, "tasks.update": true,
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "run":
		err = runTasks(os.Args[2:])
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mcp-eval:", err)
		os.Exit(1)
	}
}

// newEvalServer builds the real MCP server, with the CLI pointed at the fake mailbox.
func newEvalServer(fake *fakeServer, home string, taskTools bool) *mcp.Server {
	configFile := filepath.Join(home, "config.toml")
	_ = os.WriteFile(configFile, []byte("[safety]\ndelete_protection = [\"mail.delete\", \"contacts\", \"events\", \"tasks\", \"files\", \"folders\"]\n"), 0o600)
	sessionFile := filepath.Join(home, "session.json")
	getenv := func(key string) string {
		switch key {
		case "MBX_HOME":
			return home
		case "MBX_USERNAME":
			return ownerAddress
		case "MBX_PASSWORD":
			return "fake-password"
		}
		return ""
	}
	return mcpserver.New(mcpserver.Options{
		Run: func(stdout, stderr io.Writer, args []string, getenv func(string) string) int {
			return cli.Run(stdout, stderr, args, fake.Client(), fake.URL+"/ajax", configFile, sessionFile, getenv)
		},
		Getenv:        getenv,
		AllowedWrites: evalWrites,
		TaskTools:     taskTools,
		Version:       "eval",
	})
}

func serve(args []string) error {
	set := flag.NewFlagSet("serve", flag.ContinueOnError)
	logPath := set.String("log", "", "append each fake mailbox request as JSON to this file")
	taskTools := set.Bool("task-tools", true, "offer the typed task tools")
	if err := set.Parse(args); err != nil {
		return err
	}
	fake, err := newFakeServer(*logPath)
	if err != nil {
		return err
	}
	defer fake.Close()
	home, err := os.MkdirTemp("", "mbx-mcp-eval-home-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(home)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = newEvalServer(fake, home, *taskTools).Run(ctx, &mcp.StdioTransport{})
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// task is one evaluation task with its success checks.
type task struct {
	Name   string  `json:"name"`
	Prompt string  `json:"prompt"`
	Checks []check `json:"checks"`
}

// check passes when a logged request matches (or, with Absent, when none does) and the
// final answer contains every AnswerContains text, compared without case.
type check struct {
	Module         string   `json:"module,omitempty"`
	Action         string   `json:"action,omitempty"`
	Method         string   `json:"method,omitempty"`
	Query          string   `json:"query,omitempty"`
	BodyContains   []string `json:"body_contains,omitempty"`
	Absent         bool     `json:"absent,omitempty"`
	AnswerContains []string `json:"answer_contains,omitempty"`
}

type result struct {
	Task       string         `json:"task"`
	Variant    string         `json:"variant"`
	Success    bool           `json:"success"`
	Failed     []string       `json:"failed_checks,omitempty"`
	ToolCalls  int            `json:"tool_calls"`
	ToolErrors int            `json:"tool_errors"`
	Errors     []string       `json:"errors,omitempty"`
	Tools      map[string]int `json:"tools"`
	Turns      int            `json:"turns"`
	CostUSD    float64        `json:"cost_usd"`
	Seconds    float64        `json:"seconds"`
	Answer     string         `json:"answer"`
}

func runTasks(args []string) error {
	set := flag.NewFlagSet("run", flag.ContinueOnError)
	tasksPath := set.String("tasks", "scripts/mcp-eval/tasks.json", "task file")
	variants := set.String("variants", "task-tools,generic", "server variants: task-tools, generic")
	model := set.String("model", "haiku", "model for claude -p")
	repeat := set.Int("repeat", 1, "runs per task and variant")
	only := set.String("only", "", "comma-separated task names to run")
	out := set.String("out", "", "write all results as JSON to this file")
	if err := set.Parse(args); err != nil {
		return err
	}
	data, err := os.ReadFile(*tasksPath)
	if err != nil {
		return err
	}
	var tasks []task
	if err := json.Unmarshal(data, &tasks); err != nil {
		return fmt.Errorf("%s: %w", *tasksPath, err)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "mbx-mcp-eval-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	var results []result
	for _, variant := range strings.Split(*variants, ",") {
		if variant != "task-tools" && variant != "generic" {
			return fmt.Errorf("unknown variant %q", variant)
		}
		for _, t := range tasks {
			if *only != "" && !contains(strings.Split(*only, ","), t.Name) {
				continue
			}
			for i := 0; i < *repeat; i++ {
				r, err := runOne(t, variant, *model, self, work, fmt.Sprintf("%s-%s-%d", variant, t.Name, i))
				if err != nil {
					return fmt.Errorf("%s/%s: %w", variant, t.Name, err)
				}
				fmt.Fprintf(os.Stderr, "%-10s %-18s success=%-5v calls=%d errors=%d\n", variant, t.Name, r.Success, r.ToolCalls, r.ToolErrors)
				results = append(results, r)
			}
		}
	}
	printSummary(os.Stdout, results)
	if *out != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		return os.WriteFile(*out, append(data, '\n'), 0o644)
	}
	return nil
}

func runOne(t task, variant, model, self, work, id string) (result, error) {
	dir := filepath.Join(work, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return result{}, err
	}
	logPath := filepath.Join(dir, "requests.jsonl")
	config := map[string]any{"mcpServers": map[string]any{"mbx": map[string]any{
		"command": self,
		"args":    []string{"serve", "--log", logPath, "--task-tools=" + fmt.Sprint(variant == "task-tools")},
	}}}
	configPath := filepath.Join(dir, "mcp.json")
	data, _ := json.Marshal(config)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return result{}, err
	}
	cmd := exec.Command("claude", "-p", t.Prompt,
		"--mcp-config", configPath, "--strict-mcp-config", "--tools", "",
		"--allowedTools", "mcp__mbx", "--output-format", "stream-json", "--verbose",
		"--model", model, "--max-turns", "25", "--no-session-persistence")
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	start := time.Now()
	runErr := cmd.Run()
	r := parseStream(stdout.Bytes())
	r.Task, r.Variant, r.Seconds = t.Name, variant, time.Since(start).Seconds()
	if runErr != nil && r.Answer == "" {
		return r, fmt.Errorf("claude -p failed: %v", runErr)
	}
	requests, _ := readLog(logPath)
	r.Success = true
	for i, c := range t.Checks {
		if !c.passes(requests, r.Answer) {
			r.Success = false
			r.Failed = append(r.Failed, fmt.Sprintf("check %d", i+1))
		}
	}
	return r, nil
}

// parseStream reads the stream-json output of claude -p.
func parseStream(data []byte) result {
	r := result{Tools: map[string]int{}}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1<<20), 16<<20)
	for scanner.Scan() {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type    string          `json:"type"`
					Name    string          `json:"name"`
					IsError bool            `json:"is_error"`
					Content json.RawMessage `json:"content"`
				} `json:"content"`
			} `json:"message"`
			Result   string  `json:"result"`
			NumTurns int     `json:"num_turns"`
			Cost     float64 `json:"total_cost_usd"`
		}
		if json.Unmarshal(scanner.Bytes(), &event) != nil {
			continue
		}
		switch event.Type {
		case "assistant":
			for _, block := range event.Message.Content {
				if block.Type == "tool_use" {
					r.ToolCalls++
					r.Tools[strings.TrimPrefix(block.Name, "mcp__mbx__")]++
				}
			}
		case "user":
			for _, block := range event.Message.Content {
				if block.Type == "tool_result" && block.IsError {
					r.ToolErrors++
					r.Errors = append(r.Errors, errorText(block.Content))
				}
			}
		case "result":
			r.Answer, r.Turns, r.CostUSD = event.Result, event.NumTurns, event.Cost
		}
	}
	return r
}

func readLog(path string) ([]loggedRequest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var requests []loggedRequest
	for _, line := range bytes.Split(data, []byte("\n")) {
		var request loggedRequest
		if json.Unmarshal(line, &request) == nil {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func (c check) passes(requests []loggedRequest, answer string) bool {
	for _, want := range c.AnswerContains {
		if !strings.Contains(strings.ToLower(answer), strings.ToLower(want)) {
			return false
		}
	}
	if c.Module == "" {
		return true
	}
	found := false
	for _, r := range requests {
		if r.Module != c.Module || (c.Action != "" && r.Action != c.Action) || (c.Method != "" && r.Method != c.Method) {
			continue
		}
		if c.Query != "" {
			key, value, _ := strings.Cut(c.Query, "=")
			if r.Query[key] != value {
				continue
			}
		}
		matches := true
		for _, text := range c.BodyContains {
			if !strings.Contains(r.Body, text) {
				matches = false
			}
		}
		if matches {
			found = true
		}
	}
	return found != c.Absent
}

func printSummary(w io.Writer, results []result) {
	type key struct{ task, variant string }
	sums := map[key][]result{}
	var tasks, variants []string
	for _, r := range results {
		k := key{r.Task, r.Variant}
		if _, ok := sums[k]; !ok {
			if !contains(tasks, r.Task) {
				tasks = append(tasks, r.Task)
			}
			if !contains(variants, r.Variant) {
				variants = append(variants, r.Variant)
			}
		}
		sums[k] = append(sums[k], r)
	}
	sort.Strings(variants)
	fmt.Fprintf(w, "\n| Task |")
	for _, v := range variants {
		fmt.Fprintf(w, " %s: calls (errors), success |", v)
	}
	fmt.Fprintf(w, "\n| --- |%s\n", strings.Repeat(" --- |", len(variants)))
	totals := map[string][3]float64{}
	for _, t := range tasks {
		fmt.Fprintf(w, "| %s |", t)
		for _, v := range variants {
			runs := sums[key{t, v}]
			var calls, errs, ok float64
			for _, r := range runs {
				calls += float64(r.ToolCalls)
				errs += float64(r.ToolErrors)
				if r.Success {
					ok++
				}
			}
			n := float64(len(runs))
			fmt.Fprintf(w, " %.1f (%.1f), %d/%d |", calls/n, errs/n, int(ok), len(runs))
			sum := totals[v]
			totals[v] = [3]float64{sum[0] + calls, sum[1] + ok, sum[2] + n}
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "| **all** |")
	for _, v := range variants {
		total := totals[v]
		fmt.Fprintf(w, " %.1f calls per task, %d/%d |", total[0]/total[2], int(total[1]), int(total[2]))
	}
	fmt.Fprintln(w)
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// errorText shortens a tool result (a string or a list of text blocks) for the report.
func errorText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		var blocks []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &blocks)
		for _, block := range blocks {
			text += block.Text + " "
		}
	}
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	return strings.TrimSpace(text)
}
