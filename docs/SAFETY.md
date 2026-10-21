# Safety

`mbx` can change and delete data in your mailbox. This page explains the checks that
guard each change, and the settings to use before an agent or script runs `mbx`.

Use `mbx describe <operation> --llm` to inspect `effect` and `idempotent`. When a
command offers `--dry-run`, validate first:

```sh
mbx tasks create --title 'Example' --dry-run --llm
```

Destructive operations require `--confirm` to exactly repeat the documented target.
Objects using optimistic locking also require the latest timestamp from a preceding
read.

Before exposing `mbx` to an LLM or another automated caller, configure delete
protection in the user-level config file:

```toml
[safety]

# Block every recognized destructive request.
delete_protection = ["all"]

# Alternatively, protect only mail and folders:
# delete_protection = ["mail", "folders"]
```

The guard is checked before authentication or network access and has no command-line
override. Protecting mail also rejects `mail move --to default0/Trash` and any
`mail move-many` plan containing that destination, because those are equivalent to
deleting the message. `batch run` accepts registered reads only, so raw batch mutations
are rejected independently of delete protection. Other mail destinations remain ordinary writes.
Protected commands may still use `--dry-run`. Use exact operation names such as
`contacts.delete` for narrower protection, and run `mbx help delete-protection` for the
complete syntax. Protection is disabled when the key is omitted or set to an empty
array. Project-local config is intentionally unsupported, so a checked-out repository
cannot weaken the user policy.

To refuse every change, not only deletion, turn on read-only mode:

```toml
[safety]
read_only = true
```

`MBX_READ_ONLY=1` turns it on for one process; it cannot turn off a configured
`read_only = true`. Read-only mode refuses every write or destructive operation and every
flag that changes data by itself, such as `mail read --mark-seen`, before authentication
or network access, with the error code `read_only`. Reads, `--dry-run`, `--preflight`,
`login` and `logout` still work. Run `mbx help read-only` for details.

This policy guards immediate data deletion. It does not interpret the dynamic
`actioncmds` in mail-filter create/update requests that may affect future delivery, and
it does not classify folder-permission or group-membership replacement as deletion.
Review those ordinary writes separately when using the CLI through an automated caller.

Do not retry a non-idempotent write only because output was interrupted. Read the
resulting state first. `mail send` should only target recipients explicitly authorized
by the user; use `mail draft` when delivery is not authorized. `mail senders` lists the
addresses the account may send from, and `--from` on `mail draft` and `mail send` picks
one of them.

For repeatable folder setup, prefer `folders ensure`; it returns `{id,created}` and does
not create a duplicate exact child. `folders create` and `mail move` expose the exact
normalized provider plan in `--dry-run`, while `--preflight` authenticates and validates
the current source/parent and destination without mutating. Add `mail move --verify` to
compare the safe metadata fingerprint before and after the move.

For large move plans, use:

```sh
mbx mail move-many --input @moves.json --verify --checkpoint moves.jsonl
```

It authenticates once, resolves destinations once, paces sequential moves,
verifies each fingerprint, and appends a durable owner-only checkpoint before continuing.
Rerunning the same plan revalidates checkpoint destinations and skips completed moves.
Use `--output <file>` when the result may exceed the bounded stdout limit.

Fingerprints and checkpoints from older mbx versions: mbx now reads mail dates in UTC.
On an account whose time zone is not UTC, a fingerprint or a checkpoint that an older
mbx version made does not match. The move then stops before it changes anything:
`mail move --expect-fingerprint` fails with `precondition_failed`, and `mail move-many`
fails with `checkpoint_invalid`. Get a new fingerprint with `mail get`. For an old
checkpoint, check the destination messages by hand, remove the completed moves from the
plan, and use a new checkpoint file.
