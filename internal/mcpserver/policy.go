// ABOUTME: MCP exposure policy: which operations and flags a model may use through MCP.
// ABOUTME: Keeps credentials, local files and process stdin out of reach of tool calls.
package mcpserver

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
)

// Tool names. Each operation belongs to exactly one run tool, chosen by its effect, so a
// client can approve reads once and still confirm every write or deletion.
const (
	toolCommands = "mbx_commands"
	toolDescribe = "mbx_describe"
	toolRead     = "mbx_read"
	toolWrite    = "mbx_write"
	toolDelete   = "mbx_delete"
)

// blockedOperations handle credentials or the CLI's own session. A model never needs
// them: the person signs in once with "mbx login" in a terminal, or the server process
// gets MBX_PASSWORD.
var blockedOperations = map[string]string{
	"login":           "sign in with \"mbx login\" in a terminal, or give the MCP server MBX_PASSWORD",
	"logout":          "run \"mbx logout\" in a terminal",
	"change-password": "run \"mbx change-password\" in a terminal",
}

// fileOnlyOperations cannot succeed without a blocked flag, although the registry does
// not mark that flag as required: structured exports need --output, and a live
// mail.move-many needs --checkpoint.
var fileOnlyOperations = map[string]bool{"contacts.export": true, "events.export": true, "mail.move-many": true}

// blockedArgs read or write files on the server's machine, read its stdin (the MCP
// stdio channel), or carry a password through the model. The CLI keeps them; MCP does
// not offer them.
var blockedArgs = map[string]string{
	"save-to":          "writes a local file",
	"file":             "reads a local file",
	"output":           "writes a local file",
	"attach":           "reads a local file",
	"checkpoint":       "writes a local file",
	"password":         "passes a password through the model",
	"password-stdin":   "reads the server's stdin",
	"password-env":     "reads a password from the server environment",
	"current-password": "passes a password through the model",
	"new-password":     "passes a password through the model",
}

// serverArgs are set by the MCP server itself, from its own result limit, so a model
// cannot give them: max-output-bytes is the server's result limit, and fit makes a mail
// list return the newest messages that fit in it instead of failing.
var serverArgs = map[string]bool{"max-output-bytes": true, "fit": true}

// hiddenArg reports whether MCP leaves name out of schemas, hints and descriptions.
func hiddenArg(name string) bool {
	_, blocked := blockedArgs[name]
	return blocked || serverArgs[name]
}

// atFileArgs accept "@path" to read a local file. Through MCP they accept inline JSON only.
var atFileArgs = map[string]bool{"input": true, "permissions": true}

// inputAttachmentOperations accept local file paths in the "attachments" field of
// their --input JSON.
var inputAttachmentOperations = map[string]bool{"mail.send": true, "mail.draft": true}

// toolFor returns the run tool for op, or "" when MCP does not offer op. An operation
// is not offered when it is blocked or when one of its required flags is blocked, since
// no call could then succeed.
func toolFor(op operations.Operation) string {
	if _, blocked := blockedOperations[op.Name]; blocked || fileOnlyOperations[op.Name] {
		return ""
	}
	for name, spec := range op.Args {
		if _, blocked := blockedArgs[name]; blocked && spec.Required {
			return ""
		}
	}
	switch op.SideEffect {
	case operations.SideEffectRead:
		return toolRead
	case operations.SideEffectWrite:
		return toolWrite
	case operations.SideEffectDestructive:
		return toolDelete
	}
	return ""
}

// writeFlags returns the flags of op that change data although op itself is a read,
// for example mail.read --mark-seen.
func writeFlags(op operations.Operation) []string {
	var names []string
	for name, spec := range op.Args {
		if spec.SideEffect != "" && spec.SideEffect != operations.SideEffectRead {
			names = append(names, name)
		}
	}
	return names
}

// changesData reports whether op, or one of its flags, can change account data.
func changesData(op operations.Operation) bool {
	tool := toolFor(op)
	return tool == toolWrite || tool == toolDelete || (tool == toolRead && len(writeFlags(op)) > 0)
}

// operationsForTool lists the operation names a run tool accepts, in registry order.
// writeAllowed limits the operations that change data.
func operationsForTool(tool string, writeAllowed func(operations.Operation) bool) []string {
	var names []string
	for _, op := range operations.Default.List() {
		home := toolFor(op)
		if home == tool && (tool == toolRead || writeAllowed(op)) {
			names = append(names, op.Name)
		} else if tool == toolWrite && home == toolRead && len(writeFlags(op)) > 0 && writeAllowed(op) {
			names = append(names, op.Name)
		}
	}
	return names
}

// ValidateAllowedWrites checks the names of mcp.allowed_writes: each must be an
// operation that MCP offers and that can change data.
func ValidateAllowedWrites(names []string) error {
	for _, name := range names {
		op, ok := operations.Default.Get(strings.TrimSpace(name))
		if !ok {
			return fmt.Errorf("mcp.allowed_writes names unknown operation %q; run \"mbx commands\"", name)
		}
		if toolFor(op) == "" {
			return fmt.Errorf("mcp.allowed_writes names %s, which MCP never offers", op.Name)
		}
		if !changesData(op) {
			return fmt.Errorf("mcp.allowed_writes names %s, which only reads; reads are always offered", op.Name)
		}
	}
	return nil
}

// checkArg refuses one flag that MCP does not offer, or a value that would make the CLI
// read a local file.
func checkArg(op operations.Operation, name string, values []string) error {
	if reason, blocked := blockedArgs[name]; blocked {
		return fmt.Errorf("argument %q of %s is not available through MCP: it %s; use the mbx CLI directly", name, op.Name, reason)
	}
	if serverArgs[name] {
		return fmt.Errorf("argument %q of %s is set by the MCP server from its result limit; narrow the result with limit, select or a time range instead", name, op.Name)
	}
	if atFileArgs[name] {
		for _, value := range values {
			if strings.HasPrefix(value, "@") {
				return fmt.Errorf("argument %q of %s: @file values are not available through MCP; give the JSON inline", name, op.Name)
			}
		}
	}
	if name == "input" && inputAttachmentOperations[op.Name] {
		for _, value := range values {
			var fields map[string]json.RawMessage
			if json.Unmarshal([]byte(value), &fields) != nil {
				continue // the CLI reports the invalid JSON
			}
			for key := range fields {
				// encoding/json matches struct fields without regard to case.
				if strings.EqualFold(key, "attachments") {
					return fmt.Errorf("argument \"input\" of %s: the attachments field reads local files and is not available through MCP", op.Name)
				}
			}
		}
	}
	return nil
}
