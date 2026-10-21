# mbx: unofficial mailbox.org CLI and MCP server

`mbx` is a command-line client for the [mailbox.org](https://mailbox.org) Open-Xchange
API. It reads and manages mail, contacts, calendars, tasks, reminders, files and settings
from your terminal, from scripts, and from AI assistants such as Claude and ChatGPT
through its built-in MCP server.

This project is not affiliated with mailbox.org or Open-Xchange.

## Features

- **87 operations** for mail, contacts, calendars, tasks, reminders, files, shares,
  settings, filters, folders, groups, resources, attachments and external accounts.
- **Made for humans and agents**: readable tables, or compact JSON, JSONL, TSV and plain
  values with field selection, so a script or a model gets only the data it needs.
- **Self-describing**: `mbx commands --search "<task>"` finds the operation for a task,
  and `mbx describe` shows its arguments, effect and output fields.
- **Safe changes**: dry runs, exact confirmation for deletions, delete protection and a
  read-only mode that are checked before any network request.
- **Polite to the server**: shared request pacing across processes, failed-login backoff
  and `429` cooldowns that every process respects.
- **MCP server** for Claude Code, Claude Desktop, Claude web and ChatGPT, with typed tools
  for frequent tasks and a write allowlist.
- **Agent skill** for Codex and Claude Code, and a Claude Code plugin.

## Install

With Go 1.25 or newer:

```sh
go install github.com/hy26v/unofficial-mailboxorg-api-client/cmd/mbx@latest
```

Or download a binary for Linux, macOS or Windows from the
[releases page](https://github.com/hy26v/unofficial-mailboxorg-api-client/releases); each
release has a `SHA256SUMS` file. To build from a checkout, run `go build -o mbx ./cmd/mbx`.
`mbx version` shows the version you run.

## Quick start

Create `~/.mbx/config.toml` (`%USERPROFILE%\.mbx\config.toml` on Windows). Turn on delete
protection before you let a script or an agent use `mbx`:

```toml
username = "you@mailbox.org"

[safety]
delete_protection = ["all"]
```

Sign in once. `mbx` asks for the password with hidden input and keeps a session in
`~/.mbx/session.json`; the password never goes into the config file:

```sh
mbx login
mbx whoami
```

Then work with your mailbox:

```sh
mbx mail list --limit 10                                  # newest messages in the inbox
mbx mail search --query invoice --folder 1 --recursive    # search every mail folder
mbx mail read --folder default0/INBOX --id 1234           # body text and attachment list
mbx mail correspondents --query example.org               # find someone's address
mbx mail draft --to bob@example.com --subject Hi --body "Hello Bob"
mbx events list                                           # the next 30 days
mbx contacts list --format jsonl --select display_name,email
```

`mbx tutorial` walks through the basics, and `mbx help <command>` explains each command
without a network request.

## Use it with AI assistants

`mbx mcp` serves `mbx` over the Model Context Protocol. Each tool call runs one `mbx`
command with the same session, pacing, delete protection and read-only rules as the CLI.

- **Claude Code**: `/plugin marketplace add hy26v/unofficial-mailboxorg-api-client`, then
  `/plugin install mbx@mbx`. Or add only the server: `claude mcp add mbx -- mbx mcp`.
- **Claude Desktop**: open the `.mcpb` extension for your platform from the releases
  page. It starts in read-only mode.
- **Claude web, mobile and ChatGPT**: run `mbx mcp --http` behind HTTPS, with its own
  OAuth sign-in or Cloudflare Access; a Docker setup with a Cloudflare tunnel is included.

Mail contains text from other people, and a model can follow instructions hidden in it.
Start read-only (`mbx mcp --read-only`), and allow only the changes you need, for example
drafts but no sending. See [MCP server and Claude setup](docs/MCP.md) and
[Run the remote MCP server in Docker](docs/DOCKER.md).

## Documentation

| Topic | Where |
| --- | --- |
| Sign-in, configuration, discovery and output formats | [docs/USAGE.md](docs/USAGE.md) |
| Dry runs, confirmation, delete protection and read-only mode | [docs/SAFETY.md](docs/SAFETY.md) |
| Request pacing, backoff and the measured provider limits | [docs/RATE_LIMITING.md](docs/RATE_LIMITING.md) |
| MCP server, Claude Code, Claude Desktop and remote clients | [docs/MCP.md](docs/MCP.md) |
| Remote MCP server in Docker | [docs/DOCKER.md](docs/DOCKER.md) |
| Supported API surface | [docs/API_COVERAGE.md](docs/API_COVERAGE.md) |
| Agent skill for Codex and Claude Code | [.agents/skills/use-mailboxorg-cli](.agents/skills/use-mailboxorg-cli/SKILL.md) |
| Live tests against your own test account | [docs/LIVE_TESTING.md](docs/LIVE_TESTING.md) |

## Contributing, security and license

Bug reports, ideas and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md).
The regular test suite needs no mailbox.org account: `go test ./...`. Report
vulnerabilities privately as described in [SECURITY.md](SECURITY.md). `mbx` is released
under the [MIT License](LICENSE).
