// ABOUTME: Tests for the mbx MCP server through an in-memory MCP client session.
// ABOUTME: Covers tool listing, argument building, the exposure policy and CLI safety rules.
package mcpserver

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/cli"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/testutil"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect starts server on an in-memory transport and returns a connected client session.
func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

// recordingRunner captures every CLI invocation and answers with fixed output.
type recordingRunner struct {
	calls  [][]string
	envs   []func(string) string
	stdout string
	stderr string
	code   int
}

func (r *recordingRunner) run(stdout, stderr io.Writer, args []string, getenv func(string) string) int {
	r.calls = append(r.calls, args)
	r.envs = append(r.envs, getenv)
	io.WriteString(stdout, r.stdout)
	io.WriteString(stderr, r.stderr)
	return r.code
}

func noEnv(string) string { return "" }

func call(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", tool, err)
	}
	var texts []string
	for _, content := range result.Content {
		text, ok := content.(*mcp.TextContent)
		if !ok {
			t.Fatalf("content type = %T, want text", content)
		}
		texts = append(texts, text.Text)
	}
	return result, strings.Join(texts, "\n")
}

func errorCode(t *testing.T, text string) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(strings.SplitN(text, "\n", 2)[0]), &body); err != nil {
		t.Fatalf("error text is not JSON: %q", text)
	}
	return body.Error.Code
}

func listTools(t *testing.T, session *mcp.ClientSession) map[string]*mcp.Tool {
	t.Helper()
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	tools := map[string]*mcp.Tool{}
	for _, tool := range result.Tools {
		tools[tool.Name] = tool
	}
	return tools
}

func operationEnum(t *testing.T, tool *mcp.Tool) []string {
	t.Helper()
	schema := tool.InputSchema.(map[string]any)
	operation := schema["properties"].(map[string]any)["operation"].(map[string]any)
	var names []string
	for _, name := range operation["enum"].([]any) {
		names = append(names, name.(string))
	}
	return names
}

func TestNew_OffersFiveToolsWithEffectAnnotations(t *testing.T) {
	runner := &recordingRunner{}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))
	tools := listTools(t, session)

	if len(tools) != 5 {
		t.Fatalf("tools = %v, want 5", tools)
	}
	for _, name := range []string{toolCommands, toolDescribe, toolRead} {
		if a := tools[name].Annotations; a == nil || !a.ReadOnlyHint {
			t.Errorf("%s annotations = %+v, want read-only", name, a)
		}
	}
	if a := tools[toolWrite].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
		t.Errorf("%s annotations = %+v, want a non-destructive write", toolWrite, a)
	}
	if a := tools[toolDelete].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint {
		t.Errorf("%s annotations = %+v, want destructive", toolDelete, a)
	}

	read := operationEnum(t, tools[toolRead])
	write := operationEnum(t, tools[toolWrite])
	del := operationEnum(t, tools[toolDelete])
	for _, want := range []string{"whoami", "mail.list", "mail.read", "contacts.list"} {
		if !slices.Contains(read, want) {
			t.Errorf("mbx_read enum lacks %s", want)
		}
	}
	for _, want := range []string{"mail.send", "mail.move", "tasks.create", "mail.read"} {
		if !slices.Contains(write, want) {
			t.Errorf("mbx_write enum lacks %s", want)
		}
	}
	for _, want := range []string{"mail.delete", "contacts.delete", "folders.delete"} {
		if !slices.Contains(del, want) {
			t.Errorf("mbx_delete enum lacks %s", want)
		}
	}
	all := append(append(append([]string{}, read...), write...), del...)
	for _, absent := range []string{
		"login", "logout", "change-password", // credentials and session
		"mail.attachment", "files.create", "attachments.create", "contacts.import", "events.import", // required local files
		"contacts.export", "events.export", "mail.move-many", // need --output or --checkpoint
	} {
		if slices.Contains(all, absent) {
			t.Errorf("tool enums offer %s", absent)
		}
	}
}

func TestNew_ReadOnlyOffersOnlyReadTools(t *testing.T) {
	runner := &recordingRunner{stdout: `{"ok":true}`}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, ReadOnly: true}))
	tools := listTools(t, session)
	if len(tools) != 3 || tools[toolWrite] != nil || tools[toolDelete] != nil {
		t.Fatalf("read-only tools = %v, want commands, describe and read only", tools)
	}

	if result, _ := call(t, session, toolRead, map[string]any{"operation": "whoami"}); result.IsError {
		t.Fatal("whoami failed in read-only mode")
	}
	if got := runner.envs[0]("MBX_READ_ONLY"); got != "1" {
		t.Errorf("MBX_READ_ONLY = %q for the CLI run, want 1", got)
	}

	result, text := call(t, session, toolRead, map[string]any{"operation": "mail.read", "args": map[string]any{"folder": "f", "id": "1", "mark-seen": true}})
	if !result.IsError || errorCode(t, text) != "read_only" {
		t.Errorf("mark-seen in read-only mode = %s, want read_only error", text)
	}
	_, text = call(t, session, toolDescribe, map[string]any{"operation": "mail.delete"})
	if !strings.Contains(text, "read-only") {
		t.Errorf("describe of a delete in read-only mode = %s, want a read-only error", text)
	}
	_, text = call(t, session, toolCommands, map[string]any{"group": "mail"})
	if strings.Contains(text, "mail.delete") || !strings.Contains(text, "mail.list") {
		t.Errorf("read-only mail commands = %s", text)
	}
	if len(runner.calls) != 1 {
		t.Errorf("CLI calls = %v, want only the whoami call", runner.calls)
	}
}

func TestRunTool_BuildsCLIArguments(t *testing.T) {
	runner := &recordingRunner{stdout: `{"data":[]}`}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))

	call(t, session, toolRead, map[string]any{
		"operation": "mail.list",
		"args":      map[string]any{"folder": "--select", "limit": 5, "recursive": true, "all": false},
		"select":    "id,subject",
	})
	call(t, session, toolWrite, map[string]any{
		"operation": "mail.send",
		"args":      map[string]any{"to": []any{"a@example.invalid", "b@example.invalid"}, "subject": "Hi", "dry-run": "true"},
	})

	want := [][]string{
		{"--format", "json", "--select", "id,subject", "--", "mail", "list", "--folder", "--select", "--limit", "5", "--recursive", "--fit", "--max-output-bytes", "60000"},
		{"--format", "json", "--", "mail", "send", "--dry-run", "--subject", "Hi", "--to", "a@example.invalid", "--to", "b@example.invalid"},
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("calls = %q", runner.calls)
	}
	for i := range want {
		if !slices.Equal(runner.calls[i], want[i]) {
			t.Errorf("call %d = %q, want %q", i, runner.calls[i], want[i])
		}
	}
}

func TestRunTool_RefusesWithoutRunningTheCLI(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args map[string]any
		code string
		text string
	}{
		{"login", toolWrite, map[string]any{"operation": "login"}, "invalid_argument", "mbx login"},
		{"change-password", toolDelete, map[string]any{"operation": "change-password"}, "invalid_argument", "terminal"},
		{"required local file", toolWrite, map[string]any{"operation": "files.create"}, "invalid_argument", "local files"},
		{"unknown operation", toolRead, map[string]any{"operation": "mail.explode"}, "invalid_argument", "mbx_commands"},
		{"unknown argument", toolRead, map[string]any{"operation": "whoami", "args": map[string]any{"id": "1"}}, "invalid_argument", "takes no arguments"},
		{"dashed argument name", toolRead, map[string]any{"operation": "mail.list", "args": map[string]any{"--limit": 1}}, "invalid_argument", "without leading dashes"},
		{"save-to", toolRead, map[string]any{"operation": "files.get", "args": map[string]any{"id": "1/2", "save-to": "/tmp/x"}}, "invalid_argument", "local file"},
		{"output", toolRead, map[string]any{"operation": "mail.list", "args": map[string]any{"output": "/tmp/x"}}, "invalid_argument", "local file"},
		{"attach", toolWrite, map[string]any{"operation": "mail.send", "args": map[string]any{"attach": "/etc/passwd"}}, "invalid_argument", "local file"},
		{"password-stdin", toolWrite, map[string]any{"operation": "account.update", "args": map[string]any{"id": 1, "password-stdin": true}}, "invalid_argument", "stdin"},
		{"@file input", toolWrite, map[string]any{"operation": "contacts.create", "args": map[string]any{"input": "@/etc/passwd"}}, "invalid_argument", "@file"},
		{"@file batch", toolRead, map[string]any{"operation": "batch.run", "args": map[string]any{"input": "@plan.json"}}, "invalid_argument", "@file"},
		{"input attachments", toolWrite, map[string]any{"operation": "mail.send", "args": map[string]any{"input": `{"to":["a@example.invalid"],"Attachments":["/etc/passwd"]}`}}, "invalid_argument", "attachments"},
		{"delete on read tool", toolRead, map[string]any{"operation": "mail.delete", "args": map[string]any{"folder": "f", "id": "1", "confirm": "1"}}, "wrong_tool", "mbx_delete"},
		{"write on delete tool", toolDelete, map[string]any{"operation": "mail.send"}, "wrong_tool", "mbx_write"},
		{"read write flag on read tool", toolRead, map[string]any{"operation": "mail.read", "args": map[string]any{"folder": "f", "id": "1", "mark-seen": true}}, "wrong_tool", "mbx_write"},
		{"bad flag value", toolRead, map[string]any{"operation": "mail.list", "args": map[string]any{"recursive": "yes"}}, "invalid_argument", "true or false"},
		{"nested value", toolRead, map[string]any{"operation": "mail.list", "args": map[string]any{"folder": map[string]any{"a": 1}}}, "invalid_argument", "string, number"},
		{"unknown field", toolRead, map[string]any{"operation": "whoami", "format": "human"}, "invalid_argument", "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{}
			session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))
			result, text := call(t, session, tt.tool, tt.args)
			if !result.IsError {
				t.Fatalf("result = %s, want an error", text)
			}
			if code := errorCode(t, text); code != tt.code || !strings.Contains(text, tt.text) {
				t.Errorf("error = %s, want code %s containing %q", text, tt.code, tt.text)
			}
			if len(runner.calls) != 0 {
				t.Errorf("CLI ran %q, want no run", runner.calls)
			}
		})
	}
}

func TestRunTool_MarkSeenRunsOnWriteTool(t *testing.T) {
	runner := &recordingRunner{stdout: `{}`}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))
	result, text := call(t, session, toolWrite, map[string]any{"operation": "mail.read", "args": map[string]any{"folder": "f", "id": "1", "mark-seen": true}})
	if result.IsError || len(runner.calls) != 1 || !slices.Contains(runner.calls[0], "--mark-seen") {
		t.Fatalf("result = %s, calls = %q", text, runner.calls)
	}
	result, text = call(t, session, toolRead, map[string]any{"operation": "mail.read", "args": map[string]any{"folder": "f", "id": "1", "mark-seen": false}})
	if result.IsError || slices.Contains(runner.calls[1], "--mark-seen") {
		t.Fatalf("mark-seen false = %s, calls = %q", text, runner.calls)
	}
}

func TestRunTool_ResultMapping(t *testing.T) {
	t.Run("error output", func(t *testing.T) {
		runner := &recordingRunner{stderr: `{"error":{"code":"auth_required","message":"run mbx login"}}`, code: 3}
		session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))
		result, text := call(t, session, toolRead, map[string]any{"operation": "whoami"})
		if !result.IsError || errorCode(t, text) != "auth_required" {
			t.Errorf("result = %s, want the CLI error", text)
		}
	})
	t.Run("large read", func(t *testing.T) {
		runner := &recordingRunner{stdout: `{"data":"` + strings.Repeat("x", 200) + `"}`}
		session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, MaxReadBytes: 100}))
		result, text := call(t, session, toolRead, map[string]any{"operation": "whoami"})
		if !result.IsError || errorCode(t, text) != "result_too_large" || strings.Contains(text, "xxxx") {
			t.Errorf("result = %s, want result_too_large without data", text)
		}
	})
	t.Run("large list says how many items fit", func(t *testing.T) {
		runner := &recordingRunner{stdout: `{"tasks":[` + strings.Repeat(`{"title":"xxxxxxxxxxxxxxxxxxxx"},`, 9) + `{"title":"x"}],"truncated":false}`}
		session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, MaxReadBytes: 100}))
		_, text := call(t, session, toolRead, map[string]any{"operation": "tasks.list"})
		if errorCode(t, text) != "result_too_large" || !strings.Contains(text, "holds 10 items, of which about 2 fit") {
			t.Errorf("result = %s, want the item count and how many fit", text)
		}
	})
	t.Run("large write still returns the result", func(t *testing.T) {
		body := `{"data":"` + strings.Repeat("x", 200) + `"}`
		runner := &recordingRunner{stdout: body}
		session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, MaxReadBytes: 100}))
		result, text := call(t, session, toolWrite, map[string]any{"operation": "tasks.create", "args": map[string]any{"title": "x"}})
		if result.IsError || text != body {
			t.Errorf("result = %s, want the full write result", text)
		}
	})
	t.Run("warning and projection failure", func(t *testing.T) {
		runner := &recordingRunner{stdout: `{"id":"1"}`, stderr: `{"error":{"code":"output_projection_failed","message":"x"}}`, code: 5}
		session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))
		result, text := call(t, session, toolWrite, map[string]any{"operation": "tasks.create", "args": map[string]any{"title": "x"}})
		if !result.IsError || len(result.Content) != 2 || !strings.HasPrefix(text, `{"id":"1"}`) {
			t.Errorf("result = %s, want the result and the error", text)
		}
	})
}

func TestDiscoveryTools(t *testing.T) {
	runner := &recordingRunner{}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))

	_, text := call(t, session, toolCommands, nil)
	if !strings.Contains(text, `"name":"mail"`) {
		t.Errorf("groups = %s, want mail", text)
	}
	_, text = call(t, session, toolCommands, map[string]any{"group": "mail"})
	if !strings.Contains(text, `"name":"mail.delete","summary"`) || !strings.Contains(text, `"tool":"mbx_delete"`) || strings.Contains(text, "mail.attachment") {
		t.Errorf("mail commands = %s", text)
	}
	result, text := call(t, session, toolCommands, map[string]any{"group": "nope"})
	if !result.IsError {
		t.Errorf("unknown group = %s, want an error", text)
	}

	_, text = call(t, session, toolDescribe, map[string]any{"operation": "mail.send"})
	var described struct {
		Tool string                    `json:"tool"`
		Args map[string]argDescription `json:"args"`
	}
	if err := json.Unmarshal([]byte(text), &described); err != nil {
		t.Fatalf("describe output %q: %v", text, err)
	}
	if described.Tool != toolWrite || described.Args["to"].Type == "" || described.Args["attach"].Type != "" {
		t.Errorf("describe mail.send = %s, want tool mbx_write and no attach argument", text)
	}
	_, text = call(t, session, toolDescribe, map[string]any{"operation": "mail.read"})
	if !strings.Contains(text, "mark-seen") || !strings.Contains(text, "mbx_write") {
		t.Errorf("describe mail.read = %s, want the mark-seen note", text)
	}
	if len(runner.calls) != 0 {
		t.Errorf("discovery ran the CLI: %q", runner.calls)
	}
}

// cliRunner binds cli.Run to a fake mailbox.org server and a private config directory.
func cliRunner(t *testing.T, srv *testutil.Server, config string) Runner {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	if config != "" {
		if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sessionPath := filepath.Join(root, "session.json")
	return func(stdout, stderr io.Writer, args []string, getenv func(string) string) int {
		return cli.Run(stdout, stderr, args, srv.Client(), srv.URL+"/ajax", configPath, sessionPath, getenv)
	}
}

func fakeAccountServer(t *testing.T) *testutil.Server {
	t.Helper()
	srv := testutil.NewServer()
	t.Cleanup(srv.Close)
	srv.Handle("/ajax/login", "login", testutil.Response{Body: map[string]any{"session": "sess-1"}})
	srv.Handle("/ajax/user", "get", testutil.Response{Body: map[string]any{"data": map[string]any{
		"email1": "fixture-user@example.invalid", "user_id": 42, "display_name": "MCP Test",
	}}})
	return srv
}

func accountEnv(key string) string {
	return map[string]string{"MBX_USERNAME": "fixture-user@example.invalid", "MBX_PASSWORD": "pw"}[key]
}

func TestCLIIntegration_ReadWithSelect(t *testing.T) {
	srv := fakeAccountServer(t)
	session := connect(t, New(Options{Run: cliRunner(t, srv, ""), Getenv: accountEnv}))

	result, text := call(t, session, toolRead, map[string]any{"operation": "whoami", "select": "email"})
	if result.IsError || strings.TrimSpace(text) != `{"email":"fixture-user@example.invalid"}` {
		t.Fatalf("whoami = %s (error %v)", text, result.IsError)
	}
}

func TestCLIIntegration_DeleteProtectionStillApplies(t *testing.T) {
	srv := fakeAccountServer(t)
	run := cliRunner(t, srv, "[safety]\ndelete_protection = [\"all\"]\n")
	session := connect(t, New(Options{Run: run, Getenv: accountEnv}))

	result, text := call(t, session, toolDelete, map[string]any{
		"operation": "mail.delete",
		"args":      map[string]any{"folder": "default0/INBOX", "id": "7", "confirm": "7"},
	})
	if !result.IsError || errorCode(t, text) != "delete_protected" {
		t.Fatalf("protected delete = %s, want delete_protected", text)
	}
	result, text = call(t, session, toolWrite, map[string]any{
		"operation": "mail.move",
		"args":      map[string]any{"folder": "default0/INBOX", "id": "7", "to": "default0/Trash"},
	})
	if !result.IsError || errorCode(t, text) != "delete_protected" {
		t.Fatalf("move to Trash = %s, want delete_protected", text)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("fake server got %d requests, want none", n)
	}
}

func TestCLIIntegration_ConfiguredReadOnlyRefusesWrites(t *testing.T) {
	srv := fakeAccountServer(t)
	run := cliRunner(t, srv, "[safety]\nread_only = true\n")
	session := connect(t, New(Options{Run: run, Getenv: accountEnv}))

	result, text := call(t, session, toolWrite, map[string]any{"operation": "tasks.create", "args": map[string]any{"title": "x"}})
	if !result.IsError || errorCode(t, text) != "read_only" {
		t.Fatalf("write with read_only config = %s, want read_only", text)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("fake server got %d requests, want none", n)
	}
}

func TestNew_AllowedWritesLimitsWriteTools(t *testing.T) {
	runner := &recordingRunner{stdout: `{"ok":true}`}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, AllowedWrites: map[string]bool{"mail.draft": true, "mail.move": true}}))
	tools := listTools(t, session)
	if len(tools) != 4 || tools[toolWrite] == nil || tools[toolDelete] != nil {
		t.Fatalf("tools = %v, want commands, describe, read and write", tools)
	}
	write := operationEnum(t, tools[toolWrite])
	slices.Sort(write)
	if !slices.Equal(write, []string{"mail.draft", "mail.move"}) {
		t.Errorf("mbx_write enum = %v", write)
	}
	if strings.Contains(tools[toolWrite].Description, "mail.send") {
		t.Errorf("write description mentions mail.send: %q", tools[toolWrite].Description)
	}

	result, text := call(t, session, toolWrite, map[string]any{"operation": "mail.send", "args": map[string]any{"to": "a@example.org"}})
	if !result.IsError || !strings.Contains(text, "allowed_writes") {
		t.Errorf("mail.send = %s, want an allowed_writes error", text)
	}
	for _, tool := range []string{toolRead, toolWrite} {
		result, text = call(t, session, tool, map[string]any{"operation": "mail.read", "args": map[string]any{"folder": "f", "id": "1", "mark-seen": true}})
		if !result.IsError || errorCode(t, text) != "not_allowed" {
			t.Errorf("mark-seen on %s = %s, want not_allowed", tool, text)
		}
	}
	_, text = call(t, session, toolCommands, map[string]any{"group": "mail"})
	if strings.Contains(text, `"name":"mail.send"`) || strings.Contains(text, `"name":"mail.delete"`) || !strings.Contains(text, `"name":"mail.draft"`) || !strings.Contains(text, `"name":"mail.senders"`) {
		t.Errorf("mail commands = %s", text)
	}
	if result, _ := call(t, session, toolWrite, map[string]any{"operation": "mail.move", "args": map[string]any{"folder": "default0/INBOX", "id": "1", "to": "default0/Trash"}}); result.IsError {
		t.Error("mail.move to the trash failed")
	}
	if len(runner.calls) != 1 {
		t.Errorf("CLI calls = %v, want only the mail.move call", runner.calls)
	}
}

func TestNew_EmptyAllowedWritesOffersNoWriteTools(t *testing.T) {
	session := connect(t, New(Options{Run: (&recordingRunner{}).run, Getenv: noEnv, AllowedWrites: map[string]bool{}}))
	if tools := listTools(t, session); len(tools) != 3 || tools[toolWrite] != nil {
		t.Fatalf("tools = %v, want only discovery and read", tools)
	}
}

func TestValidateAllowedWrites(t *testing.T) {
	if err := ValidateAllowedWrites([]string{"mail.draft", "mail.move", "mail.read", "contacts.delete"}); err != nil {
		t.Fatalf("valid names refused: %v", err)
	}
	for _, name := range []string{"mail.nothing", "mail.list", "login", "contacts.export"} {
		if err := ValidateAllowedWrites([]string{name}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// TestInstructions_TaskMapNamesRegisteredOperations keeps the task map in the server
// instructions in step with the registry.
func TestInstructions_TaskMapNamesRegisteredOperations(t *testing.T) {
	names := regexp.MustCompile(`\b(whoami|[a-z]+\.[a-z][a-z-]*)\b`).FindAllString(instructions, -1)
	if len(names) < 10 {
		t.Fatalf("found only %v in the instructions", names)
	}
	for _, name := range names {
		if strings.HasSuffix(name, ".org") {
			continue // example addresses
		}
		if _, ok := operations.Default.Get(name); !ok {
			t.Errorf("instructions name %q, which is not a registered operation", name)
		}
	}
}

func hintOf(t *testing.T, text string) argHint {
	t.Helper()
	lines := strings.Split(text, "\n")
	var body struct {
		Hint argHint `json:"hint"`
	}
	if len(lines) < 2 || json.Unmarshal([]byte(lines[len(lines)-1]), &body) != nil || body.Hint.Operation == "" {
		t.Fatalf("no hint in %q", text)
	}
	return body.Hint
}

func TestErrors_TeachTheNextCall(t *testing.T) {
	runner := &recordingRunner{}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv}))

	_, text := call(t, session, toolWrite, map[string]any{"operation": "mail.draft", "args": map[string]any{"subjet": "hi"}})
	if !strings.Contains(text, `did you mean \"subject\"`) {
		t.Errorf("typo error = %s", text)
	}
	hint := hintOf(t, text)
	if hint.Tool != toolWrite || !strings.Contains(hint.Args["from"], "mail.senders") || hint.Example["operation"] != "mail.draft" || !slices.Contains(hint.Output, "folder") {
		t.Errorf("hint = %+v", hint)
	}

	_, text = call(t, session, toolRead, map[string]any{"operation": "mail_search"})
	if !strings.Contains(text, `did you mean \"mail.search\"`) {
		t.Errorf("unknown operation error = %s", text)
	}

	runner.code, runner.stderr = 4, `{"error":{"code":"invalid_argument","message":"--path is required"}}`
	_, text = call(t, session, toolRead, map[string]any{"operation": "settings.get"})
	if hint := hintOf(t, text); !slices.Equal(hint.Required, []string{"path"}) || hint.Example["args"].(map[string]any)["path"] != "<path>" {
		t.Errorf("CLI argument error hint = %+v", hint)
	}
	runner.code, runner.stderr = 1, `{"error":{"code":"api_error","message":"down"}}`
	if _, text = call(t, session, toolRead, map[string]any{"operation": "whoami"}); strings.Contains(text, `"hint"`) {
		t.Errorf("non-argument error got a hint: %s", text)
	}
}

func TestCommands_QueryFindsOperationsWithArguments(t *testing.T) {
	session := connect(t, New(Options{Run: (&recordingRunner{}).run, Getenv: noEnv, AllowedWrites: map[string]bool{"mail.draft": true}}))
	var got struct {
		Commands []commandSummary `json:"commands"`
		Note     string           `json:"note"`
	}
	_, text := call(t, session, toolCommands, map[string]any{"query": "sender aliases"})
	if err := json.Unmarshal([]byte(text), &got); err != nil || len(got.Commands) == 0 || got.Commands[0].Name != "mail.senders" || got.Commands[0].Tool != toolRead {
		t.Fatalf("query result = %s", text)
	}
	_, text = call(t, session, toolCommands, map[string]any{"query": "draft"})
	got.Commands = nil
	if err := json.Unmarshal([]byte(text), &got); err != nil || got.Commands[0].Name != "mail.draft" || !slices.Contains(got.Commands[0].Optional, "from") || slices.Contains(got.Commands[0].Optional, "attach") {
		t.Fatalf("draft query = %s", text)
	}
	// Operations the server does not offer are not found.
	if _, text = call(t, session, toolCommands, map[string]any{"query": "send email"}); strings.Contains(text, `"mail.send"`) {
		t.Errorf("query offered mail.send: %s", text)
	}
	if _, text = call(t, session, toolCommands, map[string]any{"query": "zebra"}); !strings.Contains(text, "no matching operation") {
		t.Errorf("empty query result = %s", text)
	}
	_, text = call(t, session, toolCommands, map[string]any{"group": "mail"})
	if !strings.Contains(text, `"name":"mail.read","summary"`) || !strings.Contains(text, `"required":["folder","id"]`) || !strings.Contains(text, `"output":["id","folder"`) {
		t.Errorf("group listing lacks arguments: %s", text)
	}
}

func TestTaskTools(t *testing.T) {
	runner := &recordingRunner{stdout: `{"ok":true}`}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, TaskTools: true}))
	tools := listTools(t, session)
	for _, name := range []string{"mbx_mail_list", "mbx_mail_search", "mbx_mail_correspondents", "mbx_mail_read", "mbx_mail_senders", "mbx_mail_draft", "mbx_mail_move", "mbx_events_list", "mbx_contacts_list"} {
		if tools[name] == nil {
			t.Errorf("missing %s", name)
		}
	}
	if a := tools["mbx_mail_search"].Annotations; !a.ReadOnlyHint {
		t.Errorf("search annotations = %+v", a)
	}
	if a := tools["mbx_mail_draft"].Annotations; a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint {
		t.Errorf("draft annotations = %+v", a)
	}
	props := tools["mbx_mail_draft"].InputSchema.(map[string]any)["properties"].(map[string]any)
	if props["to"].(map[string]any)["type"] != "array" || props["from"].(map[string]any)["type"] != "string" || props["dry-run"].(map[string]any)["type"] != "boolean" || props["attach"] != nil {
		t.Errorf("draft schema = %v", props)
	}
	if req := tools["mbx_mail_read"].InputSchema.(map[string]any)["required"]; !slices.Equal(req.([]any), []any{"folder", "id"}) {
		t.Errorf("read required = %v", req)
	}
	if !strings.Contains(session.InitializeResult().Instructions, "mbx_mail_draft") {
		t.Error("instructions do not name the task tools")
	}

	call(t, session, "mbx_mail_search", map[string]any{"query": "invoice", "limit": 5, "select": "id,subject"})
	call(t, session, "mbx_mail_draft", map[string]any{"from": "a@example.org", "to": []string{"b@example.org"}, "subject": "hi"})
	want := [][]string{
		{"--format", "json", "--select", "id,subject", "--", "mail", "search", "--limit", "5", "--query", "invoice", "--fit", "--max-output-bytes", "60000"},
		{"--format", "json", "--", "mail", "draft", "--from", "a@example.org", "--subject", "hi", "--to", "b@example.org"},
	}
	if len(runner.calls) != 2 || !slices.Equal(runner.calls[0], want[0]) || !slices.Equal(runner.calls[1], want[1]) {
		t.Errorf("CLI calls = %q, want %q", runner.calls, want)
	}
	for _, args := range []map[string]any{{"folder": "f", "id": "1", "mark-seen": true}, {"folder": "f", "id": "1", "save-to": "/tmp/x"}} {
		if result, text := call(t, session, "mbx_mail_read", args); !result.IsError || !strings.Contains(text, `"hint"`) {
			t.Errorf("mbx_mail_read %v = %s, want an argument error with a hint", args, text)
		}
	}
	if len(runner.calls) != 2 {
		t.Errorf("refused calls ran the CLI: %q", runner.calls)
	}
}

func TestTaskTools_FollowReadOnlyAndAllowlist(t *testing.T) {
	ro := connect(t, New(Options{Run: (&recordingRunner{}).run, Getenv: noEnv, TaskTools: true, ReadOnly: true}))
	tools := listTools(t, ro)
	if tools["mbx_mail_draft"] != nil || tools["mbx_mail_move"] != nil || tools["mbx_mail_search"] == nil || len(tools) != 10 {
		t.Errorf("read-only tools = %v", tools)
	}
	if strings.Contains(ro.InitializeResult().Instructions, "mbx_mail_draft") {
		t.Error("read-only instructions name mbx_mail_draft")
	}
	limited := connect(t, New(Options{Run: (&recordingRunner{}).run, Getenv: noEnv, TaskTools: true, AllowedWrites: map[string]bool{"mail.draft": true}}))
	tools = listTools(t, limited)
	if tools["mbx_mail_draft"] == nil || tools["mbx_mail_move"] != nil {
		t.Errorf("allowlist tools = %v", tools)
	}
}

func TestTaskTools_NameRealArguments(t *testing.T) {
	for _, task := range taskTools {
		op, ok := operations.Default.Get(task.Operation)
		if !ok {
			t.Fatalf("%s: unknown operation %s", task.Name, task.Operation)
		}
		taskSchema(op, task.Args) // panics on an unknown or blocked argument
		for name, spec := range op.Args {
			if spec.Required && !slices.Contains(task.Args, name) {
				t.Errorf("%s leaves out the required argument %s", task.Name, name)
			}
		}
	}
}

func TestTaskTools_MoveVerifiesByDefaultAndListsOutputFields(t *testing.T) {
	runner := &recordingRunner{stdout: `{"ok":true}`}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, TaskTools: true}))
	if d := listTools(t, session)["mbx_mail_move"].Description; !strings.Contains(d, "Output fields: ") || !strings.Contains(d, "destination") {
		t.Errorf("description = %q", d)
	}
	call(t, session, "mbx_mail_move", map[string]any{"folder": "default0/INBOX", "id": "1", "to": "default0/Trash"})
	call(t, session, "mbx_mail_move", map[string]any{"folder": "default0/INBOX", "id": "2", "to": "default0/Trash", "verify": false})
	if len(runner.calls) != 2 || !slices.Contains(runner.calls[0], "--verify") || slices.Contains(runner.calls[1], "--verify") {
		t.Errorf("CLI calls = %q", runner.calls)
	}
}

func TestTaskTools_HintDescribesTheCalledTool(t *testing.T) {
	runner := &recordingRunner{}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, TaskTools: true}))
	result, _ := call(t, session, "mbx_mail_list", map[string]any{"query": "invoice"})
	if !result.IsError || len(result.Content) != 2 {
		t.Fatalf("result = %v, want an error with a hint", result.Content)
	}
	var hint struct{ Hint argHint }
	if err := json.Unmarshal([]byte(result.Content[1].(*mcp.TextContent).Text), &hint); err != nil {
		t.Fatal(err)
	}
	h := hint.Hint
	if h.Tool != "mbx_mail_list" || h.Args["to-domain"] == "" || h.Args["page-size"] != "" || h.Example["operation"] != nil {
		t.Errorf("hint = %+v, want only the arguments of mbx_mail_list", h)
	}
	if !strings.Contains(h.Note, "mbx_read with operation mail.list also takes") || !strings.Contains(h.Note, "page-size") {
		t.Errorf("note = %q, want the run tool's other arguments", h.Note)
	}
	if len(runner.calls) != 0 {
		t.Errorf("refused call ran the CLI: %q", runner.calls)
	}
}

func TestDescriptions_UseToolArgumentsNotCLIFlags(t *testing.T) {
	session := connect(t, New(Options{Run: (&recordingRunner{}).run, Getenv: noEnv, TaskTools: true}))
	_, text := call(t, session, toolDescribe, map[string]any{"operation": "mail.search"})
	var described struct {
		Args   map[string]argDescription
		Output map[string]any
	}
	if err := json.Unmarshal([]byte(text), &described); err != nil {
		t.Fatal(err)
	}
	for name, arg := range described.Args {
		if strings.Contains(arg.Description, "--") {
			t.Errorf("argument %s description %q names a CLI flag", name, arg.Description)
		}
	}
	for _, hidden := range []string{"max-output-bytes", "fit", "output"} {
		if _, shown := described.Args[hidden]; shown {
			t.Errorf("describe shows %s", hidden)
		}
	}
	if got := described.Args["recursive"].Description; !strings.Contains(got, "implies all, and with limit") {
		t.Errorf("recursive description = %q", got)
	}
	if got := described.Args["all"].Description; got != "return every match" {
		t.Errorf("all description = %q, want the CLI remark removed", got)
	}
	if !slices.Contains(described.Output["on_request"].([]any), any("to")) {
		t.Errorf("output = %v, want to among the on_request fields", described.Output)
	}

	tools := listTools(t, session)
	description := tools["mbx_mail_search"].Description
	if !strings.Contains(description, "Output fields: id, subject, from, date.") ||
		!strings.Contains(description, "Returned only when fields or select names them: folder, to,") ||
		!strings.Contains(description, "over 60000 bytes returns the newest messages that fit") {
		t.Errorf("mbx_mail_search description = %q", description)
	}
}

func TestServerArgs_AreRefusedFromTheModel(t *testing.T) {
	runner := &recordingRunner{}
	session := connect(t, New(Options{Run: runner.run, Getenv: noEnv, TaskTools: true}))
	for _, c := range []struct {
		tool string
		args map[string]any
	}{
		{toolRead, map[string]any{"operation": "mail.search", "args": map[string]any{"query": "x", "max-output-bytes": 1000000}}},
		{"mbx_mail_search", map[string]any{"query": "x", "fit": true}},
	} {
		result, text := call(t, session, c.tool, c.args)
		if !result.IsError || !strings.Contains(text, "set by the MCP server") {
			t.Errorf("%s %v = %s, want a refusal", c.tool, c.args, text)
		}
	}
	if len(runner.calls) != 0 {
		t.Errorf("refused calls ran the CLI: %q", runner.calls)
	}
}
