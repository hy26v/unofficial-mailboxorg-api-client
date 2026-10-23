---
name: use-mailboxorg-cli
description: Operate mailbox.org through the mbx CLI with progressive, task-scoped guidance. Use for mail, folders, contacts, calendars, tasks, reminders, files, shares, attachments, settings, filters, snippets, groups, resources, external accounts, authentication, batch requests, command discovery, or compact machine-readable output; load only the references needed for the user's requested domain and action.
---

# Use mbx with progressive disclosure

Use `mbx` instead of inventing raw mailbox.org API calls. Treat the installed CLI as the
authority for commands and flags.

## Route before reading

1. Identify the user's domain and whether the task is a read, mutation, setup issue, or
   failure investigation.
2. Read exactly the matching domain reference below, if the task has a product domain.
3. Add a cross-cutting reference only when its condition applies.
4. Do not preload every reference. For a multi-domain request, read only the domains
   actually used.

### Domain references

| User intent | Read |
|---|---|
| List, search, read, draft, send, move, flag, or delete mail, follow a conversation, or read or save mail attachments | [mail.md](references/mail.md) |
| Create, rename, inspect, or delete folders | [folders.md](references/folders.md) |
| Contacts or vCard import/export | [contacts.md](references/contacts.md) |
| Calendar events or iCalendar import/export | [calendar.md](references/calendar.md) |
| Tasks or reminders | [tasks-reminders.md](references/tasks-reminders.md) |
| Files or public share links | [files-shares.md](references/files-shares.md) |
| Contact, task, or event attachments | [attachments.md](references/attachments.md) |
| Settings, mail filters, or signature snippets | [settings-filters-snippets.md](references/settings-filters-snippets.md) |
| User profile, quota, capabilities, groups, resources, or external accounts | [administration.md](references/administration.md) |
| Multiple/batch requests | [batch.md](references/batch.md) |

### Cross-cutting references

| Condition | Read |
|---|---|
| Login, logout, config, password, or session work is requested or fails | [authentication.md](references/authentication.md) |
| The operation is unknown or output must be optimized for an agent/pipeline | [discovery-output.md](references/discovery-output.md) |
| Any data-changing, sending, importing, or deleting action is requested | [mutations.md](references/mutations.md) |
| A command has already failed | [errors.md](references/errors.md) |

## Use the smallest discovery path

- If the exact operation is known, inspect only it with
  `mbx describe <operation> --llm`.
- If only the task is known, search for it with
  `mbx commands --search "<task words>" --llm`; each hit lists its required and optional
  arguments.
- If only the resource is known, use `mbx help <resource>`.
- List top-level groups with `mbx commands --format values --select name` only when the
  resource cannot otherwise be identified, then list one group's operations with
  `mbx commands <resource> --format values --select name`.
- Add `--full` to `describe` only when the compact description lacks a needed argument
  explanation or HTTP detail.

Do not reproduce the command catalog or large help output in the response.

## Execute minimally

- Prefer `mbx` from `PATH`; inside this repository, use `go run ./cmd/mbx` if needed.
- If the `mbx_*` MCP tools are available, you may use them instead of a shell. They take
  the same operation names, flag names without dashes in `args`, and `select` for
  `--select`. Run reads with `mbx_read`, changes with `mbx_write` and deletions with
  `mbx_delete`. They do not offer login, local-file flags or `@file` input; use the CLI
  for those.
- Bound lists and time ranges.
- Request only fields needed for the task with `--select`.
- Preserve returned ids and timestamps exactly.
- Keep secrets and session values out of commands shown to the user, logs, and responses.
- Never edit, relocate, disable, or bypass `safety.delete_protection` or
  `safety.read_only`, and never unset or change `MBX_READ_ONLY`.
- Before a mutation, read [mutations.md](references/mutations.md) and follow it.
