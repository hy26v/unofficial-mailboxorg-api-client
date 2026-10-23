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

// taskTool is one typed tool for one fixed operation. Its schema offers every argument of
// the operation that MCP offers, except the flags that change data (a read tool stays a
// read) and the arguments named in Omit, with a reason; so a new operation argument
// reaches the task tool without an edit here. Defaults are given to the operation unless
// the call sets them.
type taskTool struct {
	Name        string
	Operation   string
	Title       string
	Description string
	// Omit maps an argument that the tool leaves out to the reason.
	Omit     map[string]string
	Defaults map[string]any
}

// mailIDNote is said once in every task tool that takes or returns message ids.
const mailIDNote = " Message ids are unique only within their folder: always give an id with its folder, exactly as returned."

var taskTools = []taskTool{
	{Name: "mbx_mail_list", Operation: "mail.list", Title: "List mail",
		Description: "List the newest messages of a folder (default: the inbox; folder \"all\" lists every mail folder except Trash, Spam and Drafts), newest first. Messages in Sent and Drafts include to. When has_more is true, give next_cursor as cursor for the next page." + mailIDNote},
	{Name: "mbx_mail_search", Operation: "mail.search", Title: "Search mail",
		Description: "Search messages by words in subject, addresses or body. By default it searches every mail folder except Trash, Spam and Drafts (folder \"all\"); give a folder id to search only that folder, or folder \"1\" with recursive for every folder, Trash included. Newest first; when has_more is true, give next_cursor as cursor for the next page. Each result has the id and folder that mbx_mail_read needs, and from_name; results in Sent include to. Body matches bring newsletters too: in \"subject,from\" or from-domain narrows the search; fields \"snippet\" adds the start of each body." + mailIDNote},
	{Name: "mbx_mail_correspondents", Operation: "mail.correspondents", Title: "Find email addresses",
		Description: "Find the email addresses of a person or company in past mail, for example query \"example.org\" or a name: distinct matching addresses with name and message counts, most messages first. from_count and last_from tell whether and when the address wrote to the account; to_count, cc_count and last_to when it received mail. Searches every mail folder except Trash, Spam and Drafts unless folder is given."},
	{Name: "mbx_mail_read", Operation: "mail.read", Title: "Read a message",
		Description: "Read one message's body as plain text, with links as \"text (url)\" without tracking parameters, its attachment list (ids for mbx_mail_attachment) and its routing headers: reply_to (where a reply goes), message_id, in_reply_to and references. It stays unread." + mailIDNote},
	{Name: "mbx_mail_attachment", Operation: "mail.attachment", Title: "Read an attachment",
		Description: "Get one attachment of a message by the attachment id from mbx_mail_read. content \"text\" (default) returns the text of a PDF, DOCX, XLSX, ODT, HTML or text file, with page_count, pages_without_text (scanned pages, no OCR), has_images and notes on what the text leaves out; a cut text has truncated and next_offset, to give as offset. content \"base64\" returns the file itself, up to max-bytes." + mailIDNote},
	{Name: "mbx_mail_thread", Operation: "mail.thread", Title: "List a conversation",
		Description: "List the messages of one conversation, oldest first, from every mail folder except Trash, Spam and Drafts, Sent included: the messages whose subject without Re:, Fwd: and similar prefixes is that of the given message. Messages with another subject are not found; search for them with mbx_mail_search." + mailIDNote},
	{Name: "mbx_mail_senders", Operation: "mail.senders", Title: "List sender addresses",
		Description: "List the addresses the account may send from, with the default; query filters them. Use one as from for mbx_mail_draft. A reply draft (reply-id) picks the address the original was sent to by itself."},
	{Name: "mbx_mail_draft", Operation: "mail.draft", Title: "Save a draft",
		Description: "Save a plain-text message in Drafts without sending it. To answer a message, give its reply-id and reply-folder: the draft threads (In-Reply-To, References), and to, subject (\"Re: ...\") and from default from that message; to_source says where to came from (reply-to, from or original-recipients). To revise a draft, give its id as replace-id with only the fields that change: the others come from that draft, and it moves to Trash after the new one is saved. from must be one of mbx_mail_senders; without it the default sender applies. No signature is added unless signature is given. The result repeats to, cc, subject and in_reply_to.",
		Omit:        map[string]string{"input": "the typed arguments give the same fields"}},
	{Name: "mbx_mail_move", Operation: "mail.move", Title: "Move a message",
		Description: "Move one message to another folder; to default0/Trash is the soft delete. It checks that the message arrived (verified) unless verify is false; the new id is destination.id." + mailIDNote,
		Defaults:    map[string]any{"verify": true}},
	{Name: "mbx_events_list", Operation: "events.list", Title: "List calendar events",
		Description: "List calendar events; without from and to, the next 30 days."},
	{Name: "mbx_contacts_list", Operation: "contacts.list", Title: "List contacts",
		Description: "List contacts of the default address book."},
}

// args returns the arguments that the task tool for op offers, sorted.
func (task taskTool) args(op operations.Operation) []string {
	write := map[string]bool{}
	for _, name := range writeFlags(op) {
		write[name] = true
	}
	var names []string
	for name := range op.Args {
		if _, omitted := task.Omit[name]; !omitted && !hiddenArg(name) && !write[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
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
			InputSchema: taskSchema(op, task.args(op)),
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
	if flags := writeFlags(op); len(flags) > 0 {
		sort.Strings(flags)
		text += fmt.Sprintf(" %s change data, so this tool leaves them out; mbx_write with operation %s takes them.", strings.Join(flags, ", "), op.Name)
	}
	if len(onRequest) > 0 {
		text += fmt.Sprintf(" Returned only when %s or select names them: %s.", op.Output.OnRequestArg, strings.Join(op.Output.OnRequestFields, ", "))
	}
	if len(h.serverArgs(op)) > 0 {
		text += fmt.Sprintf(" A result over %d bytes returns the newest messages that fit, with has_more, available (the number before trimming) and next_cursor; truncated is the same as has_more.", h.opts.MaxReadBytes)
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
	for _, name := range task.args(op) {
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
