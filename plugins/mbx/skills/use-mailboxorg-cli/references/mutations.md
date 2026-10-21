# Mutations

Load this guide before every data-changing, sending, importing, or deleting operation.

1. Confirm the requested mutation is authorized.
2. Inspect `effect`, `idempotent`, required arguments, and `--dry-run` support with
   `mbx describe <operation> --llm`. An argument with `"effect":"write"` (for example
   `mail read --mark-seen`) changes data even when the operation itself is a read.
3. Read the target immediately before changing it.
4. Use `--dry-run` when offered.
5. For destructive operations, pass `--confirm` exactly as documented; never infer it
   from an unrelated object.
6. Execute once.
7. Read again and verify the requested state.

Never edit, relocate, disable, or bypass `safety.delete_protection` or
`safety.read_only`, and never unset or change `MBX_READ_ONLY`. A `delete_protected` or
`read_only` result is final unless the user changes their policy outside the agent
workflow.

Do not retry a non-idempotent write because output was interrupted. First determine
whether it succeeded by reading state. Send mail only to explicitly supplied or approved
recipients; otherwise save a draft.

For test fixtures, use unique names, track every created id, and clean up in reverse
dependency order: shares/attachments, child objects, parents, then temporary folders.
After cleanup, search for the unique marker and require zero matches.
