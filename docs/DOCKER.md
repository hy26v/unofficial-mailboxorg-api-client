# Run the remote MCP server in Docker

This guide runs `mbx mcp --http` in a container on your own computer or server, with a
Cloudflare tunnel in front of it. Claude web, the Claude mobile apps, ChatGPT and other
remote MCP clients can then use your mailbox from anywhere, and your router does not
need an open port.

```text
Claude or ChatGPT ──HTTPS──> Cloudflare ──tunnel──> cloudflared ──> mbx :8808 ──> mailbox.org
                                         (outbound connection from your host)
```

Read [the remote server section](MCP.md#claude-web-mobile-and-chatgpt-remote-server)
first. It explains the risks. Mail content goes to the model provider, and messages from
other people can contain instructions for the model.

## Choose the sign-in

| Mode | Who signs you in | Use it when |
| --- | --- | --- |
| Cloudflare Access (`--auth cloudflare-access`) | Cloudflare Access with managed OAuth, with your identity provider or a one-time email code | Your domain is on Cloudflare. Recommended. |
| Built-in OAuth (default) | mbx, with the owner secret `MBX_MCP_OWNER_SECRET` | You do not use Cloudflare Access, or you use another proxy. |

With Cloudflare Access, Access registers the client, signs you in and checks its
policy. It then sends each request through the tunnel with a signed
`Cf-Access-Jwt-Assertion`. mbx serves only `/mcp` and refuses every request without a
valid assertion: the signature must come from your team keys, and the application
audience (AUD) tag, the issuer, the lifetime and the email address must match. A request
that reaches the container another way, for example from another container on the host,
gets `403`.

## What protects the server

- Cloudflare gives the TLS certificate. `cloudflared` opens an outbound connection, so
  nothing on your network accepts connections from the internet.
- Every request to `/mcp` needs an Access assertion or an mbx OAuth access token.
- The server is read-only unless `MBX_ALLOW_WRITES=true`. `[mcp] allowed_writes` in
  `config.toml` can then limit the changes to a list (see [Allow some changes](#allow-some-changes)).
- The `mbx` container runs as a non-root user, with a read-only root file system, no
  Linux capabilities and `no-new-privileges`. The image has no shell. Only the data
  directory is writable.
- With the built-in OAuth, the port is published only on `127.0.0.1`, for local checks.
  With Cloudflare Access, no port is published.

## Requirements

- Docker Engine with Compose v2.24 or newer (`docker compose version`).
- A Cloudflare account with a domain that uses Cloudflare DNS.
- A checkout of this repository. Compose builds the image from it.

## Set up

All commands run in the repository root.

1. Make the data directory and the config file. The directory keeps `config.toml`, the
   mailbox session, the request-guard state and, with the built-in OAuth,
   `mcp-oauth.json`:

   ```sh
   mkdir -m 700 mbx-data
   cp docs/config.example.toml mbx-data/config.toml
   ```

   Edit `mbx-data/config.toml`: set your `username` and check `delete_protection`. To
   use another location, set `MBX_DATA_DIR` in `.env`. Make the directory yourself, so
   that your user owns it; a directory that Docker makes belongs to root.

2. Make the tunnel in the Cloudflare dashboard:
   1. Go to **Networking** > **Tunnels** and select **Create a tunnel**. Give it a name.
   2. Select **Docker** as the environment. The page shows a command that ends with
      `--token <value>`. Copy only the token value. Do not run the command.
   3. Open the **Routes** tab, select **Add route** > **Published application**. Enter a
      subdomain and domain, for example `mbx.example.org`. Leave the path empty. Set
      **Service URL** to `http://mbx:8808`.

3. Make the settings file. `.env` contains secrets, so only you must be able to read it:

   ```sh
   cp .env.example .env
   chmod 600 .env
   ```

   In `.env`, set:

   | Variable | Value |
   | --- | --- |
   | `MBX_PUBLIC_URL` | The tunnel address, for example `https://mbx.example.org` |
   | `CLOUDFLARE_TUNNEL_TOKEN` | The token from step 2 |
   | `MBX_UID`, `MBX_GID` | The output of `id -u` and `id -g`, the owner of `mbx-data` |

4. Set up the sign-in that you chose.

   **Cloudflare Access:**
   1. Go to **Zero Trust** > **Access controls** > **Applications** and add a
      **Self-hosted** application for the same hostname, without a path.
   2. Add a policy that allows only your email address, and select your identity
      provider (a one-time email code is enough).
   3. On the **Advanced settings** tab, turn on **Managed OAuth**. Under
      **Allowed redirect URIs**, add the callbacks of your clients, for Claude
      `https://claude.ai/api/mcp/auth_callback` and
      `https://claude.com/api/mcp/auth_callback`. Save.
   4. Select **Configure** for the application. Under **Additional settings**, copy the
      **Application Audience (AUD) Tag**.
   5. In `.env`, uncomment the four Access lines. Set `MBX_ACCESS_TEAM_DOMAIN` to your
      team domain (`<team-name>.cloudflareaccess.com`), `MBX_ACCESS_AUD` to the AUD tag
      and `MBX_ACCESS_EMAIL` to the email address in the policy. Leave
      `MBX_MCP_OWNER_SECRET` empty.

   **Built-in OAuth:** set `MBX_MCP_OWNER_SECRET` to the output of
   `openssl rand -base64 32` (at least 32 characters). Keep it in your password manager.
   You type it each time you approve a client.

5. Choose how the server signs in to mailbox.org:
   - **Password in `.env`.** Set `MBX_USERNAME` and `MBX_PASSWORD`. The server signs in
     again when the session expires. This works best for a server that runs for a long
     time, but the password is on the disk of the host.
   - **Session only.** Leave both empty and sign in once:

     ```sh
     docker compose run --rm mbx login
     ```

     The password is not stored. When the session expires, the tools return an error
     until you run the command again.

6. Build and start the server and the tunnel:

   ```sh
   docker compose up -d --build
   docker compose logs mbx
   ```

   The log shows `serving MCP at https://mbx.example.org/mcp ... (read-only)`, with
   `Cloudflare Access` in the parentheses in that mode. It also shows a warning that the
   server listens on more than loopback. This is correct in a container: `cloudflared`
   reaches it through the container network.

7. Check the public address:

   ```sh
   curl -s -o /dev/null -w '%{http_code}\n' -X POST https://mbx.example.org/mcp
   ```

   The result is `401`, because the request has no token. With Cloudflare Access,
   `curl -si -X POST https://mbx.example.org/mcp | grep -i www-authenticate` shows the
   OAuth metadata address of Access. With the built-in OAuth,
   `curl https://mbx.example.org/.well-known/oauth-protected-resource/mcp` returns JSON
   with your address as `resource`.

8. Add the connector in Claude or ChatGPT with the URL `https://mbx.example.org/mcp`, as
   in step 4 of [the remote server section](MCP.md#claude-web-mobile-and-chatgpt-remote-server).
   Your browser then opens the Access login, or the mbx approval page with the built-in
   OAuth.

## Allow some changes

The server is read-only by default. Set `MBX_ALLOW_WRITES=true` in `.env` and run
`docker compose up -d` to offer changes. Before you do, limit them in
`mbx-data/config.toml`. Reads are always offered. MCP then offers only the listed
operations that change data, and an empty list offers none:

```toml
[safety]
# Every destructive operation except mail moves into the trash.
delete_protection = [
  "account", "attachments", "contacts", "events", "files", "folders", "group",
  "mail.delete", "mailfilter", "reminder", "resources", "share", "snippet", "tasks",
]

[mcp]
# Drafts but no sending; deletion only as a move into the trash.
allowed_writes = [
  "mail.draft", "mail.move", "mail.copy", "mail.flag", "mail.read",
  "folders.create", "folders.ensure",
  "contacts.create", "contacts.update",
  "events.create", "events.update",
  "tasks.create", "tasks.update", "reminder.create",
]
```

With this example, the model can save drafts and move mail into `default0/Trash`, but
it cannot send mail or delete anything permanently. `mail.read` in the list allows
`mark-seen`. Leave out operations that can send mail or give access to other people:

| Operation | Why to leave it out |
| --- | --- |
| `mail.send` | Sends mail. |
| `mail.delete` | Moves to the trash, but deletes permanently when the message is already in the trash or when the account deletes mail permanently. Use `mail.move` to `default0/Trash`. |
| `mailfilter.*` | A filter can forward, redirect or discard incoming mail. |
| `share.create` | Makes a public link to a file. |
| `folders.update` | Can change folder permissions. |
| `account.*`, `settings.set`, `snippet.*`, `user.update` | Change external accounts, settings, signatures or the sender name. |
| Every other `*.delete` | Deletes permanently. |

`delete_protection` is a second barrier for the same deletions, and it also applies to
the CLI with this config. A `"mail"` selector or `["all"]` also blocks moves into the
trash, so the example names `mail.delete` instead. Messages in the trash stay until
you, or a cleanup rule of the account, empty it. Check each approval in the client.

## Use cloudflared on the host

If `cloudflared` already runs on the host, remove `COMPOSE_PROFILES=tunnel` from `.env`.
Compose then starts only `mbx`. Set the **Service URL** of the route to
`http://127.0.0.1:8808` (or the `MBX_HOST_PORT` you chose). This needs the published
port, so use the built-in OAuth, or remove the `ports: !reset []` line from
`compose.access.yaml`. The same works for another reverse proxy with TLS, for example
Caddy with `reverse_proxy 127.0.0.1:8808`.

## Operate

| Task | Command or action |
| --- | --- |
| Show the logs | `docker compose logs -f mbx cloudflared` |
| Stop | `docker compose down` |
| Update | `git pull`, then `docker compose up -d --build` |
| Sign in again (session only) | `docker compose run --rm mbx login` |
| Run another mbx command | `docker compose run --rm mbx whoami` |
| Disconnect every client (Access) | Remove the email address from the policy, or revoke your sessions in Zero Trust. Access tokens last at most 15 minutes, and Access checks the policy at each refresh |
| Disconnect every client (built-in OAuth) | `docker compose stop mbx`, delete `mbx-data/mcp-oauth.json`, then `docker compose start mbx` |
| Change the owner secret (built-in OAuth) | Edit `.env`, then `docker compose up -d`. Existing clients stay connected; disconnect them as above if the old secret leaked |
| Change the public address | Edit `MBX_PUBLIC_URL`, the tunnel route and the Access application, run `docker compose up -d`, then add the connectors again |

Treat `mbx-data` like a password: it contains the mailbox session and, with the built-in
OAuth, the client state. Back it up only to a place where nobody else can read it. To
put the version in the image, set `MBX_VERSION=v1.2.3` in `.env` before you build.
