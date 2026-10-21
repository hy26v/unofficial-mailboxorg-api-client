# Discovery and output

Load this guide only when the operation is uncertain or output needs optimization for an
agent, script, or pipeline.

Use the narrowest discovery command:

```sh
# Exact operation known
mbx describe <operation> --llm

# Only resource known
mbx help <resource>

# Resource unknown
mbx commands --format values --select name

# Resource known, operation unknown
mbx commands <resource> --format values --select name
```

Use `describe --full --llm` only when compact metadata lacks a needed argument
description or HTTP detail.

Choose output by consumer:

| Need | Flags |
|---|---|
| Compact single document | `--format json` |
| One object at a time | `--format jsonl` |
| Repeated rows | `--format tsv --select a,b` |
| One or a few scalar fields | `--format values --select a,b` |
| Existing compact contract | `--llm` |

Prefer explicit `--select` fields and dotted paths. Bound lists with `--limit` and time
ranges where supported. Use export commands' `--output <file>` for raw vCard or
iCalendar instead of structured formatting.

An invalid structured selector includes `allowed_fields` and a close-match `suggestion`;
use those fields to repair the invocation instead of rediscovering the whole registry.
Large `batch.run`, `mail.list --all`, and `mail.move-many` results have bounded stdout and
support owner-only `--output <file>` storage.
