// ABOUTME: Human-oriented CLI help, tutorials, operation details, and typo suggestions.
// ABOUTME: Keeps credential setup, safe mutation habits, and agent output modes discoverable.
package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	cliErrors "github.com/hy26v/unofficial-mailboxorg-api-client/internal/errors"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/operations"
	"github.com/hy26v/unofficial-mailboxorg-api-client/internal/render"
)

func helpRequest(args []string) ([]string, bool) {
	if len(args) == 0 {
		return nil, true
	}
	switch args[0] {
	case "help":
		return args[1:], true
	case "tutorial":
		return append([]string{"tutorial"}, args[1:]...), true
	case "-h", "--help":
		return nil, true
	}

	// A -h or --help given as the value of a flag (for example --subject --help) is not a
	// help request. For an unknown command op is empty, so no argument is taken as a value.
	// The command name before the flags never starts with "--", so scanning from 0 is safe.
	op, _, _ := operationForArgs(args)
	helpAt := -1
	scanOperationArgs(args, op, 0, func(token flagToken) bool {
		if token.arg == "-h" || token.arg == "--help" {
			helpAt = token.index
			return true
		}
		return false
	})
	if helpAt < 0 {
		return nil, false
	}
	parts := args[:helpAt]
	if len(parts) >= 2 {
		if _, ok := operations.Default.Get(parts[0] + "." + parts[1]); ok {
			return []string{parts[0] + "." + parts[1]}, true
		}
	}
	if len(parts) > 0 {
		return []string{parts[0]}, true
	}
	return nil, true
}

func runHelp(stdout, stderr io.Writer, target []string) int {
	if len(target) == 0 {
		writeGeneralHelp(stdout)
		return 0
	}

	topic := strings.ToLower(strings.Join(target, "."))
	switch topic {
	case "tutorial", "getting-started", "start":
		writeTutorial(stdout)
		return 0
	case "output", "formats", "llm":
		writeOutputHelp(stdout)
		return 0
	case "auth", "authentication", "login":
		writeAuthHelp(stdout)
		return 0
	case "rate-limit", "rate-limits", "burst", "throttling":
		writeRateLimitHelp(stdout)
		return 0
	// "settings" is not an alias here: it names the settings resource, whose commands
	// (settings get/set) the resource lookup below lists.
	case "config", "configuration":
		writeConfigHelp(stdout)
		return 0
	case "safety", "mutations", "dry-run":
		writeSafetyHelp(stdout)
		return 0
	case "delete-protection", "protection":
		writeDeleteProtectionHelp(stdout)
		return 0
	case "read-only", "readonly":
		writeReadOnlyHelp(stdout)
		return 0
	case "mcp":
		writeMCPHelp(stdout)
		return 0
	case "commands", "discovery", "describe":
		writeDiscoveryHelp(stdout)
		return 0
	}

	if op, ok := operations.Default.Get(topic); ok {
		writeOperationHelp(stdout, op)
		return 0
	}

	resource := strings.TrimSuffix(topic, ".")
	var matches []operations.Operation
	for _, op := range operations.Default.List() {
		if op.Resource == resource || strings.HasPrefix(op.Name, resource+".") {
			matches = append(matches, op)
		}
	}
	if len(matches) > 0 {
		fmt.Fprintf(stdout, "%s commands\n\n", resource)
		for _, op := range matches {
			fmt.Fprintf(stdout, "  %-24s %s\n", op.Name, op.Summary)
		}
		fmt.Fprintf(stdout, "\nRun `mbx help %s.<action>` for arguments and safety details.\n", resource)
		return 0
	}

	suggestion := closestHelpName(topic)
	message := "unknown help topic or command: " + topic
	if suggestion != "" {
		message += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	err := &cliErrors.Error{Code: "invalid_argument", Message: message}
	writeErr(stderr, false, err)
	return cliErrors.ExitCodeFor(err)
}

func writeGeneralHelp(w io.Writer) {
	fmt.Fprint(w, `mbx — mailbox.org CLI for humans and automated agents

Usage:
  mbx <resource> <action> [flags]
  mbx <command> [flags]
  mbx help [topic|command]
  mbx tutorial

Getting started:
  1. Put your safety policy in ~/.mbx/config.toml.
  2. Run mbx login; it saves a missing email and prompts for the hidden password.
     Non-interactive automation supplies MBX_PASSWORD instead.
  3. Discover safely: mbx commands
  4. Drill into a group: mbx commands contacts
  5. Inspect one command: mbx help contacts.list

Agent output:
  --llm                    Legacy compact JSON
  --format <json|jsonl|tsv|values>
  --select <field,...>     Return only needed fields; dotted paths are supported

Useful help topics:
  mbx help config          Cross-platform config file and setting reference
  mbx help auth            Credentials and session behavior
  mbx help rate-limits     Request pacing, login backoff, and 429 handling
  mbx help output          Token-efficient output recipes
  mbx help safety          Dry runs and destructive confirmations
  mbx help delete-protection
                            Config-enforced destructive-operation blocking
  mbx help read-only       Refuse every command and flag that changes data
  mbx help mcp             Serve mbx to Claude, ChatGPT and other MCP clients
  mbx help commands        Command discovery
  mbx tutorial             End-to-end guided workflow
  mbx version              Show the mbx version

Run "mbx commands" to list command groups, then "mbx commands <group>" for operations.
`)
}

func writeTutorial(w io.Writer) {
	fmt.Fprint(w, `mbx tutorial

1. Configure and authenticate
   Create ~/.mbx/config.toml with a safety policy. The username is optional here;
   interactive login asks for and saves it when missing:

     username = "you@mailbox.org"

     [safety]
     delete_protection = ["all"]

   mbx login

2. Discover the smallest useful command set
   mbx commands --format values --select name
   mbx commands contacts --format values --select name
   mbx describe contacts.list --llm
   mbx help contacts.list

3. Read data with token-efficient output
   mbx mail list --folder default0/INBOX --limit 10 \
     --format tsv --select id,subject,date
   mbx mail list --folder default0/INBOX --all --page-size 200 \
     --fields received_date,seen,flagged --format jsonl
   mbx contacts list --format jsonl --select id,display_name,email

4. Mutate safely
   Use --dry-run whenever the command offers it. Destructive commands also require
   --confirm with the exact id or account value shown by "mbx help <command>".
   Before giving mbx to an agent, configure safety.delete_protection = ["all"], or
   protect selected resources with ["mail", "folders"].

5. Recover and continue
   Errors go to stderr. Structured modes return {"error":{...}} there. Retry only when
   retryable is true and wait at least retry_after_ms when present; inspect a command
   again after invalid_argument errors.

More: "mbx help output", "mbx help safety", "mbx help auth", "mbx help rate-limits".
`)
}

func writeOutputHelp(w io.Writer) {
	fmt.Fprint(w, `Machine and agent output

  --llm
      Backward-compatible compact JSON.

  --format json
      Compact JSON; combine with --select to remove unused fields.

  --format jsonl
      One collection item per line. A single array wrapper is removed; scalar list
      metadata such as count or truncated is omitted.

  --format tsv
      Header plus tab-separated rows. Best when many objects share the same fields.

  --format values
      Headerless tab-separated values. Smallest mode; use an explicit --select.

  --select id,subject
      Project comma-separated fields before rendering. Dotted paths such as owner.email
      are supported. Selection is validated against the command's documented output
      (the dry-run fields with --dry-run); structured failures include allowed_fields
      and a close-match suggestion. If a write succeeds but the selection still fails,
      stdout gets the full result, stderr an output_projection_failed error, and the
      exit code is 5; do not repeat the write.

  --
      Stop parsing global output flags. Use this before a command value that is literally
      named --llm, --format, or --select.

Examples:
  mbx commands --format values --select name
  mbx commands mail --format values --select name
  mbx mail list --format tsv --select id,subject,date
  mbx whoami --format values --select email
  mbx mail draft --format json --subject -- --llm --dry-run

Raw vCard/iCal exports are intentionally not transformed; use their --output file flag.
`)
}

func writeAuthHelp(w io.Writer) {
	fmt.Fprint(w, `Authentication and sessions

Put the non-secret username in ~/.mbx/config.toml:

  username = "you@mailbox.org"

Run "mbx login" from an interactive terminal. If username is missing, it first asks for
the mailbox address and atomically saves it to config.toml with mode 0600. It then asks
for the password with terminal echo disabled. The prompted password stays in this
process only: it is not added to the OS environment, config, or command arguments. For
non-interactive automation, configure the username and supply MBX_PASSWORD only in the
process environment. A non-interactive login with missing credentials fails instead of
waiting for input. Passwords are deliberately rejected from config.toml.

Successful login creates a stay-signed-in mailbox.org session and stores its id/cookies
separately at ~/.mbx/session.json (mode 0600).

Normal commands try cookie-based autologin first and do not need MBX_PASSWORD while the
stored session remains reusable. If it expires, run "mbx login" again; non-interactive
automation may instead provide environment credentials as a fallback. The session is
reused only for the account that created it: after a username change, or with a session
file from an older mbx, run "mbx login" again (or supply MBX_PASSWORD). "mbx logout"
invalidates the server session and clears the local store; a later "mbx login" creates
a fresh session. Never print, commit, or share the session file; treat it like a password.

Full credential logins are serialized across processes sharing MBX_HOME. Rejected
credentials trigger exponential local backoff; see "mbx help rate-limits".
`)
}

func writeRateLimitHelp(w io.Writer) {
	fmt.Fprint(w, `Rate limits and burst protection

Every network request is paced through shared state in MBX_HOME/request-guard.json.
Request starts are at least 250 milliseconds apart across CLI processes using that same
MBX_HOME: a sustained four HTTP requests per second, or 1,200 per five minutes.
A batch run is limited to 20 sub-requests, all backed by registered read operations;
each consumes one shared pacing slot, so /multiple cannot bypass the sustained budget.
Explicit typed decoders keep raw provider payloads from bypassing command safety.

Full credential logins are also serialized. After rejected credentials, mbx delays the
next real login by 2, 4, 8, 16, 32, then at most 60 seconds. A successful credential
login resets that backoff. This prevents a stale password in unattended automation from
rapidly exhausting mailbox.org's failed-login allowance.

When the provider returns HTTP 429, mbx shares Retry-After with every process using the
same MBX_HOME. If the header is missing or invalid, it uses a 60-second cooldown. No
cooldown is longer than one hour, including one already stored in the state file. When a
provider returns login-limit code LGI-0026 or LGI-0027 without an HTTP 429, mbx applies
a conservative 10-minute credential-login cooldown. When a cooldown cannot fit within
the command timeout, structured output reports:

  code: rate_limited
  retryable: true
  retry_after_ms: <remaining delay>

mbx never automatically retries an API operation. In particular, it will not replay a
write whose result may be ambiguous. Wait for retry_after_ms and verify current state
before manually retrying a non-idempotent write.

Separate MBX_HOME directories have separate budgets. Automation aimed at one account
and endpoint should share one MBX_HOME if it needs coordinated protection. The guard
state contains timestamps and counters, not credentials, and is written mode 0600.
`)
}

func writeConfigHelp(w io.Writer) {
	fmt.Fprint(w, `Configuration

mbx reads one user-level TOML file:

  ~/.mbx/config.toml

Go resolves the current user's home directory and native path separators, so the same
location model works on Linux, macOS, and Windows. Set MBX_HOME to an absolute directory
only when portable automation or isolated tests need to relocate all mbx config/state.

Example:

  username = "you@mailbox.org"

  [safety]
  delete_protection = ["all"]
  # Optional: refuse every command and flag that changes account data.
  # read_only = true

  # Optional; relative paths resolve from the directory containing config.toml.
  [state]
  session_file = "session.json"

Use delete_protection = ["mail", "folders"] for selected resources, or [] (or omit the
key) to disable it. Exact destructive operation names such as "contacts.delete" are
also accepted.

The parser rejects unknown keys, malformed TOML, oversized files, and password fields.
Secrets and session cookies are not stored in config.toml. Project-local config and
command-line config overrides are intentionally unsupported so repository content or an
agent invocation cannot weaken user-level delete protection.

An interactive "mbx login" adds username when it is absent. It changes only that key and
keeps the rest of the file, including comments. If config.toml is a symbolic link, mbx
updates the file that the link points to. It never writes the password.
`)
}

func writeSafetyHelp(w io.Writer) {
	fmt.Fprint(w, `Mutation safety

Operations are classified as read, write, or destructive in "mbx describe".
Commands offering --dry-run validate and show intent without calling the mutation
endpoint. Destructive operations require --confirm to exactly match the target value.

For a user-config-enforced guard that confirmation flags cannot override, see
"mbx help delete-protection". To refuse every change, see "mbx help read-only".

Recommended sequence:
  1. mbx describe <command> --llm
  2. Run with --dry-run when available.
  3. Use --preflight when offered to authenticate and validate current remote state.
  4. Read the target immediately before changing it.
  5. Execute once; do not retry a non-idempotent write unless the error proves it failed.
  6. Read again to verify the resulting state. For mail.move, use --verify.

For a large mail plan, use:
  mbx mail move-many --input @moves.json --verify --checkpoint moves.jsonl

The checkpoint is owner-only and append-only. A rerun revalidates completed destinations
before skipping them, so it does not replay a verified move.
`)
}

func writeDeleteProtectionHelp(w io.Writer) {
	fmt.Fprint(w, `Delete protection

Configure safety.delete_protection in ~/.mbx/config.toml:

  [safety]
  delete_protection = ["all"]
      Block every recognized destructive request.

  [safety]
  delete_protection = ["mail", "folders"]
      Block destructive operations only for the named resources. Exact operation names,
      such as contacts.delete, may also be selected.

  [safety]
  delete_protection = []
      Disable protection. Omitting the setting also disables it.

The policy is checked before authentication or network access. There is no command-line
override, so an agent cannot bypass it by adding another flag. --dry-run and --preflight
remain available. Protecting mail blocks "mail move --to default0/Trash" and any
"mail move-many" plan containing that destination. Moves to other folders remain allowed.
"batch run" accepts registered read operations only and rejects every mutation regardless
of this setting. Unknown selectors fail closed for destructive commands.

The guard covers immediate data deletion. It does not interpret mail-filter actioncmds
that may affect future delivery, or classify folder-permission and group-membership
replacement as deletion. Review those ordinary writes separately.

This guard reduces accidental data loss; it is not a sandbox for an agent that can alter
the user config file or replace the mbx executable.
`)
}

func writeReadOnlyHelp(w io.Writer) {
	fmt.Fprint(w, `Read-only mode

Turn it on in ~/.mbx/config.toml:

  [safety]
  read_only = true

or for one process with MBX_READ_ONLY=1. The variable can only turn the mode on; it
cannot turn off read_only = true in the config. Any value other than 1/0, true/false,
yes/no or on/off is an error.

In read-only mode, mbx refuses before authentication or network access:
  - every operation whose effect is write or destructive in "mbx describe", and
  - every flag that changes data by itself, shown as "effect write" in
    "mbx help <command>" and as "effect":"write" on the argument in
    "mbx describe <command> --llm" (for example mail read --mark-seen).

The refusal has the error code read_only. Reads, --dry-run and --preflight still work.
login and logout also work, because they change only the mbx session, not account data.

This guard reduces accidental changes; it is not a sandbox for an agent that can alter
the user config file, the environment of mbx, or the mbx executable.
`)
}

func writeMCPHelp(w io.Writer) {
	fmt.Fprint(w, `MCP server

  mbx mcp [--read-only]
  mbx mcp --http <addr> --public-url <https-url> [--allow-writes] [--redirect-uri <uri>]...
  mbx mcp --http <addr> --public-url <https-url> [--allow-writes] --auth cloudflare-access
      --access-team-domain <team>.cloudflareaccess.com --access-aud <tag> --access-email <email>...

Without --http, serves mbx over MCP (Model Context Protocol) on stdin and stdout, for
Claude Code, Claude Desktop and other MCP clients. The client starts the process.

With --http, serves MCP over Streamable HTTP at <public-url>/mcp for remote clients such
as Claude web (custom connectors) and ChatGPT (developer mode), with a built-in OAuth
server. Put a tunnel or reverse proxy with TLS in front, and listen on 127.0.0.1.
MBX_MCP_OWNER_SECRET (at least 32 characters) is required: you type it on the approval
page when a client connects. Approved clients and token hashes are kept in
mcp-oauth.json next to config.toml; delete that file and restart to disconnect every
client.
Claude, ChatGPT and loopback redirect URIs are accepted; add others with --redirect-uri.
The HTTP server is read-only unless you give --allow-writes. Give it MBX_PASSWORD so it
can sign in again when the session expires.

With --auth cloudflare-access, a Cloudflare Access application with managed OAuth does
the sign-in instead, and no owner secret is needed. The server serves only /mcp and
refuses every request without a valid Cf-Access-Jwt-Assertion: the RS256 signature from
the team keys, the application audience tag, the team issuer, the lifetime and one of
the --access-email addresses must match.

Tools:
  mbx_commands   list command groups and operations (no network request)
  mbx_describe   show one operation's arguments (no network request)
  mbx_read       run an operation that only reads data
  mbx_write      run an operation that changes data but does not delete it
  mbx_delete     run an operation that deletes data

Typed tools for frequent tasks take the operation's arguments directly:
mbx_mail_list, mbx_mail_search, mbx_mail_correspondents, mbx_mail_read,
mbx_mail_attachment, mbx_mail_thread, mbx_mail_senders, mbx_events_list,
mbx_contacts_list, and, when changes are offered, mbx_mail_draft and mbx_mail_move.
Each offers every argument of its operation except flags that change data.
They follow the same read-only and mcp.allowed_writes rules. --task-tools=false
offers only the five tools above.

Each tool call runs one mbx command with the same config, session, request pacing,
delete protection and read-only rules as the CLI. Sign in first with "mbx login" in a
terminal, or give the server MBX_USERNAME and MBX_PASSWORD in its environment.

Through MCP, mbx never offers login, logout or change-password, flags that read or write
local files (such as --save-to, --file, --output, --attach) or @file input, or flags
that pass passwords. Use the CLI directly for those.

To offer only some changes, list them in config.toml; reads are always offered, and an
empty list offers no changes:

  [mcp]
  allowed_writes = ["mail.draft", "mail.move", "mail.flag"]

--read-only offers only mbx_commands, mbx_describe and mbx_read. safety.read_only in
config.toml and MBX_READ_ONLY=1 have the same effect. Without read-only mode, configure
safety.delete_protection before you connect a client. Mail and other account data
contain text from other people; a model can follow instructions hidden in that text.
`)
}

func writeDiscoveryHelp(w io.Writer) {
	fmt.Fprint(w, `Command discovery

  mbx commands
      Human list of top-level command groups such as mail, contacts, and tasks.

  mbx commands --format values --select name
      Smallest top-level command-group list for agents.

  mbx commands <group>
      List the operations in one group, for example "mbx commands tasks".

  mbx commands <group> --format values --select name
      Smallest operation-name list for one group.

  mbx commands [<group>] --search "<task words>"
      The best matching operations for a task, for example --search "unread mail",
      each with its required and optional arguments.

  mbx describe <command> --llm
      Compact usage, effect, idempotency, argument types, and output fields.

  mbx describe <command> --full --llm
      Adds argument descriptions and HTTP metadata.

  mbx help <resource>
      List operations for one resource.

  mbx help <command>
      Full human help for one operation.
`)
}

func writeOperationHelp(w io.Writer, op operations.Operation) {
	fmt.Fprintf(w, "%s\n\n%s\n\n", op.Name, op.Summary)
	fmt.Fprintf(w, "Usage: %s\n\n", fullUsageFor(op))
	fmt.Fprintf(w, "Effect: %s", op.SideEffect)
	if op.Destructive {
		fmt.Fprint(w, " (destructive)")
	}
	fmt.Fprintf(w, "\nIdempotent: %t\n", op.Idempotent)
	fmt.Fprintf(w, "API: %s %s (action %s)\n", op.Method, op.Endpoint, op.Action)

	if len(op.Args) > 0 {
		fmt.Fprint(w, "\nArguments:\n")
		fields := make([]render.Field, 0, len(op.Args))
		for _, name := range sortedArgNames(op.Args) {
			arg := op.Args[name]
			requirement := "optional"
			if arg.Required {
				requirement = "required"
			}
			if arg.SideEffect != "" && arg.SideEffect != operations.SideEffectRead {
				requirement += ", effect " + string(arg.SideEffect)
			}
			fields = append(fields, render.Field{
				Name:  "--" + name,
				Value: fmt.Sprintf("%s, %s — %s", arg.Type, requirement, arg.Description),
			})
		}
		_ = render.KeyValueTable(w, fields)
	}
	fmt.Fprintf(w, "\nOutput: %s", op.Output.Type)
	if len(op.Output.Fields) > 0 {
		fmt.Fprintf(w, " (%s)", strings.Join(op.Output.Fields, ", "))
	}
	fmt.Fprintln(w)
	if len(op.Output.DryRunFields) > 0 {
		fmt.Fprintf(w, "Dry-run output: %s\n", strings.Join(op.Output.DryRunFields, ", "))
	}
	if op.SideEffect != operations.SideEffectRead {
		fmt.Fprintln(w, "\nSafety: inspect current state first; use --dry-run when listed above.")
	} else if hasWriteFlag(op) {
		fmt.Fprintln(w, "\nSafety: a flag marked \"effect write\" changes data; read-only mode refuses it.")
	}
	fmt.Fprintf(w, "\nAgent form: mbx describe %s --llm\n", op.Name)
}

func hasWriteFlag(op operations.Operation) bool {
	for _, arg := range op.Args {
		if arg.SideEffect != "" && arg.SideEffect != operations.SideEffectRead {
			return true
		}
	}
	return false
}

func fullUsageFor(op operations.Operation) string {
	usage := clientName + " " + strings.ReplaceAll(op.Name, ".", " ")
	for _, required := range []bool{true, false} {
		for _, name := range sortedArgNames(op.Args) {
			arg := op.Args[name]
			if arg.Required != required {
				continue
			}
			flag := "--" + name
			if arg.TakesValue() {
				flag += " <" + arg.Type + ">"
			}
			if required {
				usage += " " + flag
			} else {
				usage += " [" + flag + "]"
			}
		}
	}
	return usage
}

func suggestCommand(input string) string {
	candidates := []string{"commands", "describe", "help", "mcp", "tutorial", "version"}
	for name := range resourceHandlers {
		candidates = append(candidates, name)
	}
	return closest(input, candidates)
}

func closestHelpName(input string) string {
	candidates := []string{"tutorial", "output", "auth", "rate-limits", "safety", "commands", "mcp"}
	for _, op := range operations.Default.List() {
		candidates = append(candidates, op.Name, op.Resource)
	}
	return closest(input, candidates)
}

func closest(input string, candidates []string) string {
	sort.Strings(candidates)
	best := ""
	bestDistance := len(input) + 4
	for _, candidate := range candidates {
		distance := editDistance(input, candidate)
		if distance < bestDistance {
			best = candidate
			bestDistance = distance
		}
	}
	limit := 2
	if len(input) >= 8 {
		limit = 3
	}
	if bestDistance > limit {
		return ""
	}
	return best
}

func editDistance(a, b string) int {
	previous := make([]int, len(b)+1)
	for j := range previous {
		previous[j] = j
	}
	for i := 1; i <= len(a); i++ {
		current := make([]int, len(b)+1)
		current[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			current[j] = min3(current[j-1]+1, previous[j]+1, previous[j-1]+cost)
		}
		previous = current
	}
	return previous[len(b)]
}

func min3(a, b, c int) int {
	if a < b && a < c {
		return a
	}
	if b < c {
		return b
	}
	return c
}
