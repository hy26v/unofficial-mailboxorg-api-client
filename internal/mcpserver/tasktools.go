// ABOUTME: Typed MCP tools for frequent tasks (list, search and read mail, senders, drafts, ...).
// ABOUTME: Each runs one fixed operation with a schema from the registry and the run tools' checks.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// taskTool is one typed tool. Args names the arguments its schema offers: the ones agents
// need for the task. The generic run tools keep every argument. Defaults are given to the
// operation unless the call sets them.
type taskTool struct {
	Name        string
	Operation   string
	Title       string
	Description string
	Args        []string
	Defaults    map[string]any
}

var taskTools = []taskTool{
	{"mbx_mail_list", "mail.list", "List mail", "List the newest messages of a folder (default: the inbox), newest first. Returns ids for mbx_mail_read.",
		[]string{"folder", "limit", "cursor", "since", "until", "from", "to-domain", "fields"}, nil},
	{"mbx_mail_search", "mail.search", "Search mail", "Search messages by words in subject, addresses or body (default: the inbox; folder \"1\" with recursive searches every folder, newest first). Each result has the id and folder that mbx_mail_read needs. Body matches bring newsletters too: in \"subject,from\" or from-domain narrows the search.",
		[]string{"query", "folder", "in", "limit", "cursor", "since", "until", "from", "from-domain", "to-domain", "all", "recursive", "skip-trash", "fields"}, nil},
	{"mbx_mail_correspondents", "mail.correspondents", "Find email addresses", "Find the email addresses of a person or company in past mail, for example query \"example.org\" or a name: distinct matching addresses with name, message count and last date, most messages first. Searches every mail folder unless folder is given.",
		[]string{"query", "in", "folder", "since", "until", "limit"}, nil},
	{"mbx_mail_read", "mail.read", "Read a message", "Read one message's body as plain text, with links as \"text (url)\" without tracking parameters, and its attachment list. It stays unread.",
		[]string{"folder", "id", "body-format", "links", "max-body-bytes"}, nil},
	{"mbx_mail_senders", "mail.senders", "List sender addresses", "List the addresses the account may send from, with the default; query filters them. Use one as from for mbx_mail_draft. A reply draft (reply-id) picks the address the original was sent to by itself.",
		[]string{"query", "variants"}, nil},
	{"mbx_mail_draft", "mail.draft", "Save a draft", "Save a plain-text message in Drafts without sending it. To answer a message, give its reply-id and reply-folder: the draft threads (In-Reply-To, References), and to, subject (\"Re: ...\") and from default from that message. from must be one of mbx_mail_senders; without it the default sender applies. No signature is added unless signature is given. The result repeats to, cc, subject and in_reply_to.",
		[]string{"from", "to", "cc", "bcc", "subject", "body", "reply-id", "reply-folder", "reply-all", "quote", "signature", "folder", "dry-run"}, nil},
	{"mbx_mail_move", "mail.move", "Move a message", "Move one message to another folder; to default0/Trash is the soft delete. It checks that the message arrived (verified) unless verify is false; the new id is destination.id.",
		[]string{"folder", "id", "to", "verify", "dry-run"}, map[string]any{"verify": true}},
	{"mbx_events_list", "events.list", "List calendar events", "List calendar events; without from and to, the next 30 days.",
		[]string{"from", "to", "limit", "no-expand"}, nil},
	{"mbx_contacts_list", "contacts.list", "List contacts", "List contacts of the default address book.",
		[]string{"limit"}, nil},
}

// taskToolNames lists the typed tools that addTaskTools registers.
func (h *handlers) taskToolNames() []string {
	if !h.opts.TaskTools {
		return nil
	}
	var names []string
	for _, task := range taskTools {
		if op, ok := operations.Default.Get(task.Operation); ok && h.toolFor(op) != "" {
			names = append(names, task.Name)
		}
	}
	return names
}

// addTaskTools registers the typed tools for the operations this server offers.
func (h *handlers) addTaskTools(server *mcp.Server) {
	for _, task := range taskTools {
		op, ok := operations.Default.Get(task.Operation)
		if !ok {
			panic("mcpserver: task tool " + task.Name + " names unknown operation " + task.Operation)
		}
		tool := h.toolFor(op)
		if tool == "" {
			continue // read-only mode or mcp.allowed_writes leaves it out
		}
		annotations := &mcp.ToolAnnotations{OpenWorldHint: boolPtr(true)}
		if tool == toolRead {
			annotations.ReadOnlyHint, annotations.IdempotentHint = true, true
		} else {
			annotations.DestructiveHint = boolPtr(tool == toolDelete)
		}
		server.AddTool(&mcp.Tool{
			Name:        task.Name,
			Title:       task.Title,
			Description: h.taskDescription(task, op),
			InputSchema: taskSchema(op, task.Args),
			Annotations: annotations,
		}, h.runTask(task, op, tool))
	}
}

// taskDescription adds the output fields to the task description: the default ones, the
// ones returned on request, and how a result over the byte limit is cut.
func (h *handlers) taskDescription(task taskTool, op operations.Operation) string {
	onRequest := map[string]bool{}
	for _, field := range op.Output.OnRequestFields {
		onRequest[field] = true
	}
	var fields []string
	for _, field := range op.Output.Fields {
		if !onRequest[field] {
			fields = append(fields, field)
		}
	}
	text := task.Description + " Output fields: " + strings.Join(fields, ", ") + "."
	if len(onRequest) > 0 {
		text += fmt.Sprintf(" Returned only when %s or select names them: %s.", op.Output.OnRequestArg, strings.Join(op.Output.OnRequestFields, ", "))
	}
	if len(h.serverArgs(op)) > 0 {
		text += fmt.Sprintf(" A result over %d bytes returns the newest messages that fit, with has_more, available (the number before trimming) and next_cursor.", h.opts.MaxReadBytes)
	}
	return text
}

// taskSchema builds a typed JSON schema for the listed arguments of op, plus select.
func taskSchema(op operations.Operation, names []string) map[string]any {
	properties := map[string]any{
		"select": map[string]any{"type": "string", "description": "comma-separated output fields to return; dotted paths are supported"},
	}
	var required []string
	for _, name := range names {
		spec, ok := op.Args[name]
		if !ok {
			panic(fmt.Sprintf("mcpserver: task tool for %s names unknown argument %q", op.Name, name))
		}
		if hiddenArg(name) {
			panic(fmt.Sprintf("mcpserver: task tool for %s offers hidden argument %q", op.Name, name))
		}
		property := map[string]any{"description": mcpText(spec.Description)}
		switch {
		case !spec.TakesValue():
			property["type"] = "boolean"
		case spec.Type == "int" || spec.Type == "integer":
			property["type"] = "integer"
		case strings.Contains(spec.Description, "repeatable"):
			property["type"] = "array"
			property["items"] = map[string]any{"type": "string"}
		default:
			property["type"] = "string"
		}
		properties[name] = property
		if spec.Required {
			required = append(required, name)
		}
	}
	sort.Strings(required)
	return objectSchema(properties, required...)
}

// runTask runs the fixed operation of task through the same checks as the run tools.
func (h *handlers) runTask(task taskTool, op operations.Operation, tool string) mcp.ToolHandler {
	offered := map[string]bool{}
	for _, name := range task.Args {
		offered[name] = true
	}
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var raw map[string]json.RawMessage
		if args := req.Params.Arguments; len(args) > 0 && string(args) != "null" {
			if err := json.Unmarshal(args, &raw); err != nil {
				return toolError("invalid_argument", fmt.Sprintf("invalid tool arguments: %v", err)), nil
			}
		}
		selectFields := ""
		if value, ok := raw["select"]; ok {
			if json.Unmarshal(value, &selectFields) != nil {
				return toolError("invalid_argument", "select must be a string"), nil
			}
			delete(raw, "select")
		}
		for name := range raw {
			// A hidden argument gets its own message from buildArgs.
			if !offered[name] && !hiddenArg(name) {
				return h.argumentError(op, &task, fmt.Sprintf("%s does not take %q; the hint lists its arguments, and %s with operation %s takes every argument", task.Name, name, tool, op.Name)), nil
			}
		}
		for name, value := range task.Defaults {
			if _, given := raw[name]; !given {
				data, _ := json.Marshal(value)
				if raw == nil {
					raw = map[string]json.RawMessage{}
				}
				raw[name] = data
			}
		}
		args, err := buildArgs(op, raw, selectFields)
		if err != nil {
			return h.argumentError(op, &task, err.Error()), nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return h.run(tool, op, append(args, h.serverArgs(op)...), &task), nil
	}
}
