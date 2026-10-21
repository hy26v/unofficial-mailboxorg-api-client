# Using mbx

This page explains sign-in and configuration, how to find the operation for a task, and
the output formats. Run `mbx help` and `mbx tutorial` for the same information offline.

## Sign-in and configuration

Create `~/.mbx/config.toml` (`%USERPROFILE%\.mbx\config.toml` on Windows) with your
safety policy. You may include the non-secret username now, or let interactive login ask
for it and add it to this file:

```toml
username = "you@mailbox.org"

[safety]
delete_protection = ["all"]
```

Keep the password out of the config file. On an interactive terminal, `mbx login` first
asks for and saves the mailbox address if it is missing, then asks for the password with
terminal echo disabled. It keeps the reusable session separately in
`~/.mbx/session.json`:

```sh
mbx login
mbx whoami
mbx mail list --folder default0/INBOX --limit 10
mbx logout
```

Commands after `mbx login` reuse that session and do not need the password again. If the
session expires, run `mbx login` again. The session file records the account that logged
in. If the configured username names a different account, or the file comes from an older
`mbx` version that did not record the account, `mbx` does not reuse it: it logs in again
with `MBX_PASSWORD` when that is set, and otherwise asks you to run `mbx login`. For a non-interactive fresh login or credential
fallback, supply `MBX_PASSWORD` only in the process environment. If stdin is not a
terminal and an explicit login lacks the variable, `mbx` fails instead of waiting for
input.

`mbx change-password --confirm <your email>` asks for the current and new password with
hidden input on a terminal. For automation, set `MBX_PASSWORD` (current) and
`MBX_NEW_PASSWORD` (new) in the process environment. The older `--current-password` and
`--new-password` arguments still work, but print a warning: other local users and shell
history can see command arguments.

`mbx` resolves the home directory and path separators through the operating system, so
the same configuration works on Linux, macOS, and Windows. For isolated automation,
set the single `MBX_HOME` variable to an absolute directory; it relocates `config.toml`,
`session.json`, and the request-guard state. A custom session path can instead be set with
`state.session_file` in TOML. Treat the session file like a password; it is created with
owner-only permissions where the operating system supports them.

Run the built-in tutorial or inspect one operation without making a network request:

```sh
mbx tutorial
mbx help config
mbx help output
mbx help contacts
mbx help contacts.update
```

## Discovery and output

List top-level command groups first, then drill into only the group you need:

```sh
mbx commands --format values --select name
mbx commands mail --format values --select name
```

This keeps discovery small instead of emitting the entire operation registry at once.
To go straight to the operation for a task, search by words. Each hit lists its required
and optional arguments:

```sh
mbx commands --search "sender address" --llm
```

Inspect a command before using it:

```sh
mbx describe events.create --llm
mbx describe events.create --full --llm
```

Global output flags may appear anywhere in a command:

```sh
# One compact JSON document.
mbx whoami --format json --select email,user_id

# One JSON object per row.
mbx contacts list --format jsonl --select id,display_name,email

# Header plus tab-separated rows.
mbx mail list --folder default0/INBOX --limit 20 \
  --format tsv --select id,subject,from,date

# Follow the returned cursor, or safely scan all pages as JSONL.
mbx mail list --folder default0/INBOX --cursor offset:20 --limit 20 --llm
mbx mail list --folder default0/INBOX --all --page-size 200 \
  --fields to,seen,flagged,attachment,sent_date,received_date --format jsonl

# Search subjects, addresses and body text on the server, in every mail folder.
mbx mail search --query 'invoice "March 2026"' --folder 1 --all --recursive \
  --fields folder --format tsv --select id,folder,subject,from,date

# Search only the address fields (from, to, cc and bcc); --select to also requests to.
mbx mail search --query example.org --in address --format json --select id,from,to

# Find a person's or company's addresses in all past mail, most messages first.
mbx mail correspondents --query example.org

# Read one message's body and attachment list (it stays unread), then save an attachment.
mbx mail read --folder default0/INBOX --id 1234 --format json --select subject,body,attachments
mbx mail attachment --folder default0/INBOX --id 1234 --attachment 2 --save-to ./invoice.pdf

# Headerless values, useful for shell pipelines and agents.
mbx commands --format values --select name
mbx commands tasks --format values --select name
```

`--llm` remains the backward-compatible compact JSON mode. `--select` supports dotted
paths and is validated against each operation's documented output before a mutation is
sent. In `mail list` and `mail search`, a selected field that only `--fields` returns
(such as `to` or `folder`) is added to `--fields`. `--recursive` implies `--all`, and with `--limit` it
returns the newest matches of all folders; `--all` with `--limit` and without
`--recursive` pages like `--limit` alone. A JSON projection of an incomplete
list keeps `has_more`, `truncated` and `next_cursor`. With `--fit`, `mail list` and
`mail search` return the newest messages that fit in `--max-output-bytes`, with
`available` (the number before trimming) and a `next_cursor` that resumes after them,
instead of failing with `output_too_large`. A dotted path into a field that holds a single value is rejected, and with
`--dry-run` the selector is checked against the dry-run fields shown by
`mbx describe <operation> --llm`. A structured invalid selector includes sorted
`allowed_fields` and a `suggestion` when there is a close match. If a write succeeds
but `--select` still cannot be applied to its result, mbx writes the full result to
stdout, an `output_projection_failed` error to stderr, and exits with code 5. Do not run
the write again in that case.

Raw vCard and iCalendar exports should use their `--output` flag instead of a structured
format.

`mbx` keeps each response in memory before it uses it. To protect memory, it stops with
`code: "response_too_large"` when a JSON response is larger than 256 MiB or a download
(file, attachment or export) is larger than 4 GiB.
