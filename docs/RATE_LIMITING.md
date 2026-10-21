# Rate limiting

The CLI has built-in request protection. This page records the provider evidence used
to choose its defaults and the exact behavior automation can rely on.

mailbox.org also warns that unusually many connection attempts can trigger a temporary
firewall block of roughly 15 minutes, without publishing one stable threshold. See its
[firewall guidance](https://kb.mailbox.org/en/private/faq/where-are-the-servers-located/).

## How mbx protects requests

Network requests are protected automatically. Processes using the same `MBX_HOME` share
`request-guard.json`, which spaces HTTP request starts by at least 250 ms: a sustained
four per second or 1,200 per five minutes. This is below Open-Xchange's documented
generic default of [1,500 requests per five minutes](https://software.open-xchange.com/products/appsuite/doc/Release-Notes-v7.2.2.pdf#page=14).
`batch run` accepts at most 20 registered read-only sub-requests, and each consumes one
shared pacing slot. Every supported operation has an explicit safe request builder and
typed decoder; arbitrary provider actions and all mutations are rejected before network
access. This keeps one `/multiple` call from bypassing the sustained budget while also
preventing raw provider mail bodies or headers from bypassing normal CLI projection.

Credential logins are serialized across those processes. A rejected credential login
causes exponential delays of 2, 4, 8, 16, 32, then at most 60 seconds before another real
login attempt. A successful credential login resets the delay. If mailbox.org returns
HTTP `429`, the CLI persists and honors `Retry-After` for every process sharing that
state; a missing header produces a conservative 60-second cooldown, and a cooldown never
exceeds one hour. Provider login-rate
codes `LGI-0026` and `LGI-0027` impose a 10-minute credential-login cooldown.

The guard never retries an operation automatically, because replaying a mutation after
an ambiguous response could apply it twice. If the required delay exceeds the command's
timeout, structured output returns `code: "rate_limited"`, `retryable: true`, and the
remaining `retry_after_ms`. Separate `MBX_HOME` directories have independent budgets, so
automation targeting the same account should share one home. Run `mbx help rate-limits`
for the complete runtime contract; the measurement behind these defaults follows below.

## mailbox.org measurement

On 2026-08-13, a bounded probe used the dedicated test account against
`https://office.mailbox.org/ajax`. Credentials,
session values, and response payloads were not printed. Successful probes used only the
read-only `whoami` operation; the authentication probe deliberately supplied an invalid
generated password and stopped on the first rate-limit response.

Successful authenticated reads did not encounter a low burst threshold:

- sequential runs succeeded with inter-command delays of 1,000 ms, 500 ms, 250 ms, and
  zero (50 consecutive zero-delay commands);
- simultaneous groups of 5, 10, 20, and 40 CLI processes all succeeded;
- because a normal stored-session command performs autologin and then the requested
  read, those calls represented roughly two HTTP requests per CLI command.

The failed-login boundary was concrete within the bounded run:

- failed attempts 1 through 20 returned the ordinary `LGI-0006` credential rejection;
- attempt 21 returned HTTP `429`;
- `Retry-After` was 600 seconds (`retry_after_ms: 600000`).

This is an observed deployment boundary, not a provider promise. mailbox.org can change
its configuration, and distributed middleware nodes or another source IP can produce a
different observation. Open-Xchange documents token-bucket failed-login controls and
configurable limits in its [login rate-limit documentation](https://documentation.open-xchange.com/7.10.0/middleware/login/04_ratelimit/login_rate_limit.html).
Its generic servlet limit is likewise configurable; the documented default is
[1,500 requests per five-minute sliding window](https://software.open-xchange.com/products/appsuite/doc/Release-Notes-v7.2.2.pdf#page=14).
The probe intentionally stopped after proving the first real `429` instead of attempting
a second, larger denial boundary.

## CLI protection

The shipped `mbx` process wraps its HTTP transport with these rules:

- all processes sharing an `MBX_HOME` start requests at least 250 ms apart, limiting the
  aggregate to a sustained four HTTP requests per second and 1,200 per five minutes;
- credential logins are serialized across processes;
- rejected credential logins impose delays of 2, 4, 8, 16, 32, then at most 60 seconds;
- a successful credential login resets the failed-login delay. When the provider
  redirects a login, the redirect itself counts as neither success nor failure; the
  response at the end of the redirect chain decides;
- any HTTP `429` establishes a shared cooldown from `Retry-After`, or 60 seconds if the
  header is missing or invalid. The cooldown is at most one hour: a larger value is
  far above the measured 600 seconds and the roughly 15-minute firewall block, so it is
  treated as a provider error. If the limit continues, the next request gets a new `429`;
- provider login-limit codes `LGI-0026` and `LGI-0027` establish a 10-minute
  credential-login cooldown even without an HTTP `429`;
- `batch run` accepts at most 20 registered read-only sub-requests, charges one shared
  pacing slot for each, and rejects arbitrary provider actions and mutations locally;
- requests are never retried automatically.

The state lives at `MBX_HOME/request-guard.json` with owner-only permissions. It contains
only timestamps and counters. When the CLI reads the file, it limits each stored delay to
at most one hour from now, so an old or damaged file cannot block commands for longer. If
a required wait does not fit within the command deadline, structured error output reports
`rate_limited`, `retryable: true`, and `retry_after_ms`; the provider is not contacted.
The error message names the state file that holds the delay.

The guarded build was then verified against the live endpoint:

- five simultaneous `whoami` processes all succeeded, but their roughly ten HTTP
  requests were flattened to 2.5 seconds rather than arriving together;
- two generated bad-password attempts took approximately 0.25 and 2.25 seconds;
- corrected credentials then waited approximately 4.3 seconds, succeeded, and reset the
  persisted failure count to zero;
- the repository's full safe-mode live matrix finished with 24 passes, zero provider
  limitations, zero failures, and zero cleanup failures.

The scope is deliberately the CLI home. Processes using different `MBX_HOME` values do
not coordinate, so automation against one account and endpoint should use the same home.
