# MCP server and Claude setup

`mbx mcp` serves mbx over MCP (Model Context Protocol) on stdin and stdout. MCP clients
such as Claude Code and Claude Desktop start it themselves. `mbx mcp --http` serves
remote clients such as Claude web and ChatGPT. It offers five general tools:

| Tool | Runs |
| --- | --- |
| `mbx_commands`, `mbx_describe` | Local discovery, without a network request |
| `mbx_read` | Operations that only read data |
| `mbx_write` | Operations that change data but do not delete it |
| `mbx_delete` | Operations that delete data |

Typed tools for frequent tasks take the operation's arguments directly, so an agent needs
no discovery call for them: `mbx_mail_list`, `mbx_mail_search`,
`mbx_mail_correspondents`, `mbx_mail_read`, `mbx_mail_senders`, `mbx_events_list`,
`mbx_contacts_list`, and, when changes are
offered, `mbx_mail_draft` and `mbx_mail_move`. They follow the same read-only and
allowlist rules. `mbx mcp --task-tools=false` offers only the five tools above.

Clients use the tool annotations to decide when to ask you. You can allow `mbx_read`
once and still approve each write and deletion.

Each tool call runs one mbx command in the server process, with the same config,
session, request pacing, delete protection and read-only rules as the CLI. Sign in first
with `mbx login` in a terminal, or give the server `MBX_USERNAME` and `MBX_PASSWORD` in
its environment. Through MCP, mbx does not offer `login`, `logout`, `change-password`,
flags that read or write local files (such as `--save-to`, `--file`, `--output` and
`--attach`), `@file` input, or flags that pass passwords. Use the CLI for those.
A read result holds at most 60000 bytes. The server sets `--fit` and
`--max-output-bytes` itself, so a larger mail list or search returns the newest messages
that fit; other larger reads fail with `result_too_large` and say how many items fit.

`mbx mcp --read-only`, `safety.read_only = true` and `MBX_READ_ONLY=1` offer only the
discovery and read tools. Otherwise, configure `safety.delete_protection` first. Mail and
other account data contain text from other people, and a model can follow instructions
hidden in that text. Give write access only when you need it, and check each approval.
To offer only some changes, for example drafts but no sending, list them in
`config.toml`; see [Allow some changes](DOCKER.md#allow-some-changes):

```toml
[mcp]
allowed_writes = ["mail.draft", "mail.move", "mail.flag"]
```

## Claude Code

Install `mbx` on your `PATH` (see [Install](../README.md#install)) and sign in with `mbx login`. Then
add this repository as a plugin marketplace and install the plugin:

```text
/plugin marketplace add hy26v/unofficial-mailboxorg-api-client
/plugin install mbx@mbx
```

The plugin starts `mbx mcp` and adds the agent skill. To use only the MCP server:

```sh
claude mcp add mbx -- mbx mcp
```

## Claude Desktop

Each release has one Claude Desktop extension per platform, for example
`mbx-v0.3.0-darwin-arm64.mcpb`. Open the file for your platform with Claude Desktop, or
drag it into Settings > Extensions. The extension asks for your mailbox.org address, an
optional password and read-only mode, which is on by default. Leave the password empty
to use the session from `mbx login`. Claude Desktop keeps the password in the system
keychain. The extension uses the same `~/.mbx` config and session as the CLI.

Without the extension, add `mbx` to `claude_desktop_config.json` (on macOS in
`~/Library/Application Support/Claude/`, on Windows in `%APPDATA%\Claude\`). Use the full
path, because Claude Desktop does not read your shell `PATH`:

```json
{
  "mcpServers": {
    "mbx": {
      "command": "/usr/local/bin/mbx",
      "args": ["mcp", "--read-only"]
    }
  }
}
```

Remove `--read-only` to allow changes. Restart Claude Desktop after you edit the file.
To build the extensions from a checkout, run `scripts/build-mcpb.sh v1.2.3`; it needs Go,
Python 3 and Node.js.

## Claude web, mobile and ChatGPT (remote server)

Claude web (custom connectors) and ChatGPT (developer mode) connect from their own
servers, so `mbx` must be reachable on a public HTTPS address. `mbx mcp --http` serves
MCP at `/mcp` with a built-in OAuth server for one owner: you. With
`--auth cloudflare-access`, a Cloudflare Access application with managed OAuth signs you
in instead, and mbx checks the signed Access assertion on each request. To run it in
Docker with a Cloudflare tunnel, in either mode, see
[Run the remote MCP server in Docker](DOCKER.md).

This puts your mailbox on the internet behind one secret. Read the risks first:

- Anyone who learns `MBX_MCP_OWNER_SECRET` can connect a client to your mailbox.
- The server is read-only unless you add `--allow-writes`. Keep it read-only if you can.
- Mail content goes to Anthropic or OpenAI as part of the conversation.
- Messages from other people can contain instructions for the model.

1. Run `mbx login` once on the host, or give the server `MBX_USERNAME` and
   `MBX_PASSWORD`, so it can sign in again when the session expires.
2. Choose a long random owner secret and start the server on loopback:

   ```sh
   export MBX_MCP_OWNER_SECRET="$(openssl rand -base64 32)"
   mbx mcp --http 127.0.0.1:8808 --public-url https://mbx.example.org
   ```

3. Give it TLS with a tunnel or reverse proxy, for example a Cloudflare named tunnel
   from `mbx.example.org` to `http://127.0.0.1:8808`, or Caddy with
   `reverse_proxy 127.0.0.1:8808`. A tunnel with a random address works for a test, but
   `--public-url` must match the address, and clients must connect again when it changes.
4. Add the connector:
   - Claude: Settings > Connectors > Add custom connector, URL
     `https://mbx.example.org/mcp`.
   - ChatGPT: turn on developer mode in the advanced app settings (availability depends
     on your plan), then create a connector with the same URL and OAuth authentication.
5. Your browser opens the mbx approval page. Check the application name and the return
   address, enter the owner secret and select Approve.

The owner secret must have at least 32 characters. Each wrong secret gets a one-second
delay; there is no lockout, so nobody else can lock you out. A registered client must be
approved within 30 minutes. The server accepts the Claude and ChatGPT OAuth callbacks
and loopback addresses; add another client's callback with `--redirect-uri`. Access
tokens last one hour and refresh tokens 30 days. Only their hashes are stored, in
`mcp-oauth.json` next to `config.toml`. Delete that file and restart the server to
disconnect every client. Run `mbx help mcp` for all options.
