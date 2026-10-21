# Live testing

`scripts/live-test.sh` builds the current source into a private temporary directory,
uses an isolated `MBX_HOME` for config and session state, and runs commands against
`https://office.mailbox.org/ajax`.

Use a dedicated mailbox.org test account. The runner is non-interactive and reads
credentials only from `MBX_USERNAME` and `MBX_PASSWORD`.

## Supply your own live-test account

Live tests require an active account supplied by the caller for that invocation.
The account used during initial development is being retired; no shared account or
ongoing access to it is provided with this open-source CLI. The runner has no built-in
credentials and does not load a credential file or fetch from a particular secret store.

For an interactive Bash invocation, read the credentials without putting the password
in command history, then pass them as environment inputs:

```bash
(
set +xv
read -r -p 'Test account username: ' MBX_USERNAME || exit 1
read -r -s -p 'Test account password: ' MBX_PASSWORD || exit 1
printf '\n'
export MBX_USERNAME MBX_PASSWORD
./scripts/live-test.sh safe
)
```

Automation can populate the same two environment variables from its secret store.
Run `./scripts/live-test.sh --help` for the input contract. Missing credentials fail
before building the CLI or contacting mailbox.org. Full mode still requires the
separate write opt-in described below.

The regular `go test ./...` suite uses synthetic fixtures and local fake HTTP servers.
It requires neither a mailbox.org account nor a secret store. Real credentials belong
only in explicitly invoked live tests.

## Safe mode

Run it with `./scripts/live-test.sh safe` after you supply the credentials as shown above.

Safe mode covers authentication, command decoding, representative reads, bounded list
operations, normalized folder preflight, and a typed read-only batch request whose
projected quota and authenticated-user results are matched against standalone reads. If
an Inbox message exists, it also proves that the batched mail adapter returns only its
selected safe metadata fields—not raw headers or a body. It does not create fixtures.
Its authentication lifecycle performs a fresh login, proves stored-cookie reuse in a
separate process, verifies logout and local session removal, proves the removed session
cannot be reused, then performs a fresh login and final logout.

Safe mode also reads message content. With an Inbox message, `mail.read` returns at most
1,024 body bytes, and a later `mail.get` proves the read left the seen flag unchanged.
With an Inbox message that has an attachment, `mail.attachment` saves it in the runner's
private temporary directory, compares the reported and actual byte counts, and deletes
the file. `mail.search` searches the Inbox for a word taken from a listed subject, first
in subjects (every match must contain the word) and then in bodies. The runner asserts
only shape and identity; it does not print message text.

## Full mode

Full mode requires an explicit write gate:

```sh
# Supply MBX_USERNAME and MBX_PASSWORD through the environment first.
MBX_LIVE_WRITES=1 ./scripts/live-test.sh full
```

The runner's temporary home intentionally has no delete-protection policy because full
mode must remove the fixtures it creates. This does not read or modify the user's
normal `~/.mbx/config.toml`.

It dynamically reads the registered operation list and fails if any operation is not
accounted for. Every operation is exercised except an explicitly skipped password
round-trip when `MBX_LIVE_PASSWORD_ROUNDTRIP` is not enabled. The lifecycle covers:

- fresh credential login, configured-account identity, cross-process stored-session
  reuse, verified provider/local logout, rejected reuse after clearing, fresh
  reauthentication, and a final verified logout;
- a temporary secondary account using a unique mailbox.org plus-address;
- folder, contact, attachment, event, task, reminder, file/share, group, resource,
  a newly created uniquely marked snippet (existing signatures are never reused), and
  an inactive mail-filter fixture;
- contact and calendar import/export;
- toggling one fixed-whitelist boolean UI setting, independently verifying it, then
  restoring and independently verifying the exact original value;
- a temporary display-name change followed by restoration;
- draft creation whose exact id/folder/subject/recipient metadata is independently read,
  whose subject must remain absent from Sent and Inbox, and which is rediscovered through
  rich filtered, recursively paginated JSONL output before its observed seen/flagged
  values are both flipped and independently re-read;
- folder ensure creating a top-level mail folder through normalized parent `default0`,
  verifying virtual parent `1`, reusing it without duplication, removing it from root and
  Trash, and separately reusing one exact existing infostore child; a fingerprint-guarded
  mail move with authenticated preflight and destination verification; and a separate
  move-many draft that is moved once, durably checkpointed with mode `0600`, resumed
  without replay, independently read, and removed;
- one email previewed and sent only to `MBX_USERNAME`; exactly one matching Sent and
  Inbox copy must expose the authenticated sender, approved recipient, attachment, and
  unique subject before marker-owned removal and zero-match verification.

Every fixture begins with a unique `mbx-live-<timestamp>-<pid>` marker. Cleanup runs on
normal completion, errors, interruption, and shell exit. Mail cleanup never mutates a
tracked id until a complete folder listing and metadata read prove that the current
object still carries this run's marker. A final sweep recovers ids changed or lost during
copy/move, and cleanup succeeds only after complete Inbox, Sent, Drafts, and Trash scans
contain zero fixture subjects. If a signal bypasses the normal verified authentication
ending, the exit trap makes one best-effort idempotent logout after fixture cleanup and
before removing the private session store.

The following optional variables are supported:

Every network-bearing CLI invocation, including semantic read-backs and cleanup, uses
the same interval. Set a larger value such as `2` when the provider is recovering from
rate limiting; the runner never retries or compresses the remaining requests into a
catch-up burst.

The built CLI also enforces its normal transport-level guard: HTTP starts are spaced by
at least 250 ms through the runner's isolated `MBX_HOME`, failed credential logins back
off exponentially, and provider `Retry-After` cooldowns are shared. The runner interval
is an additional test-matrix safety margin between complete CLI invocations.

| Variable | Purpose |
|---|---|
| `MBX_LIVE_TIMEOUT_SECONDS` | Per-command outer timeout; default `45` |
| `MBX_LIVE_REQUEST_INTERVAL_SECONDS` | Positive whole-second delay between live CLI invocations; default `1` |
| `MBX_LIVE_IMAP_URL` | IMAP URL used by the temporary secondary account; default `imap://imap.mailbox.org:143` |
| `MBX_LIVE_PASSWORD_ROUNDTRIP=1` | Enable a real password change followed immediately by rollback |

Password testing is a second opt-in because a failed rollback could lock out the
account. If the first change succeeds, the trap retains both values in memory and retries
the rollback before any other fixture cleanup. On the tested mailbox.org deployment the
endpoint currently returns `SRV-0001`, so no password is changed.

To distinguish cookie reuse from silent credential fallback, the runner substitutes a
generated, deliberately rejected password only in child-process environments. It first
requires `whoami` to succeed through the stored session, then, after logout and physical
session-file removal, requires one credential login with that value to fail with the
documented authentication exit code. The generated value and the real password are
never printed, written to repository files, or passed in command arguments.

## Interpreting results

- `PASS`: the real endpoint and every operation-owned semantic assertion completed.
- `LIMITED`: the request reached the real endpoint, but an external provider service
  prevented an end-to-end success.
- `SKIPPED`: the separately gated password round-trip was not requested; no endpoint
  claim is made for that operation.
- `FAIL`: a client, contract, assertion, cleanup, or unexpected server failure.

Full mode exits nonzero for failures, unexpectedly unaccounted registered operations,
or cleanup failures. Intentional password skips and provider limitations are reported
separately and do not hide passing coverage.

The runner never prints credential values or response payloads. Some CLI operations
necessarily accept password values as operation flags; run the matrix only on a trusted
machine where process arguments are not exposed to untrusted users.
