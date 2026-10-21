# Batch requests

Load this guide only when one `batch run` call is materially better than separate
commands.

`batch.run` accepts a JSON array of 1-20 registered read-only operations. Every supported
operation has an explicit provider request builder and typed safe decoder; arbitrary raw
provider actions and all mutations are rejected before authentication. The upper bound
keeps one `/multiple` call from bypassing request-burst protection, and every subrequest
consumes one shared pacing slot:

```json
[
  {"operation":"quota.get","params":{"type":"filestore"},"select":["quota","use"]},
  {"operation":"whoami","select":["email","user_id"]},
  {"operation":"mail.get","params":{"folder":"default0/INBOX","id":"7"},"select":["id","subject","fingerprint"]}
]
```

Inspect each subrequest's registered operation first. Prefer related read-only requests,
save the payload in a file when quoting would be fragile, and dry-run before sending:

```sh
mbx batch run --input @requests.json --dry-run --llm
mbx batch run --input @requests.json --output batch-results.json --llm
```

The currently batchable operations are `quota.get`, `whoami`, `mail.get`, and a bounded
`mail.list` subset (`folder`, `limit`, and `fields`). Direct `mail.list` handles cursors,
all-page scans, recursion, and filters. Full stdout is bounded; use `--output` for a large
owner-only result. Per-request `select` is validated against the operation registry;
for `mail.list`, an optional field must also be in `fields`. A sub-result that fails, or
that cannot be decoded or projected, becomes that entry's `error`. The other results stay
and the exit code is 0, so check each entry.

Use specialized mutation commands instead of batching writes. In particular,
`mail.move-many` owns verification and resumable checkpointing; generic batch execution
cannot provide that recovery contract.
