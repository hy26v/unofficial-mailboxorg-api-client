# Error handling

Load this guide only after a command fails.

Structured modes write an error envelope to stderr:

```json
{"error":{"code":"...","message":"...","http_status":0,"retryable":false}}
```

- Exit `2`: repair authentication using the authentication guide.
  This includes `SVL-0003` ("The provided cookie is invalid"): the saved session
  expired, so run `mbx login`; do not retry.
- Exit `3`: re-read or search for the target.
- Exit `4`: inspect the exact operation and correct flags or selected fields.
- Exit `5` (`output_projection_failed`): the write succeeded, but `--select` could not be
  applied. stdout has the full result. Do not repeat the write.
- `network_error` (exit `1`, retryable): the sign-in request did not reach the provider.
  The credentials were not rejected, so do not log in again; retry later.
- `delete_protected`: stop; do not bypass or weaken the policy.
- `read_only`: read-only mode is on. Stop; do not change the config or `MBX_READ_ONLY`.
  Run the command without the write flag named in the message, or report that the
  change is not allowed.
- `share_link_left_behind`: `share delete` created a new public link and could not remove
  it. The link is public now. Run the `share delete` command from the message once, then
  report the URL to the user if that also fails.
- `reminder_not_found`: `reminder create` created the task, but no single reminder matches
  it. Do not run the command again; that creates a second task. Delete the task named in
  the message if it is not wanted.
- `checkpoint_write_failed`: `mail move-many` moved and verified the message named in
  `failed`, but could not record it in the checkpoint. Fix the checkpoint file problem
  (disk space, permissions) and remove that move from the plan before a rerun.
- `response_too_large`: the response was larger than the memory limit (256 MiB JSON,
  4 GiB download). Do not retry; narrow the request.
- Other failures: use the API code and message. For `rate_limited`, wait at least
  `retry_after_ms`; the local guard may have rejected the command before network access.

Retry only when `retryable` is true, with bounded backoff. Never retry a
non-idempotent mutation until a read proves it did not succeed. Keep stdout reserved for
successful data and stderr for diagnostics.
