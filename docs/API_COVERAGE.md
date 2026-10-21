# API coverage notes

The CLI currently registers 87 operations. The 2026-08-17 full matrix accounted for the
82 operations registered then: 81 passed end to end, the separately gated password round
trip was skipped, and there were zero failures or cleanup failures. The two operations
added before that matrix and after the earlier 80-operation matrix are `folders.ensure`
and `mail.move-many`. The `mail.search`, `mail.read`, and `mail.attachment` operations
(read-only, except `mail.read --mark-seen`) were added after the 2026-08-17 matrix; the
live runner's safe section covers them, but no recorded live run includes them yet.
`mail.senders` (read-only) was added later still, with `--from` on `mail.send` and
`mail.draft`, and then `mail.correspondents` (read-only); no recorded live run includes
them yet.

Coverage here means the supported `mbx` surface, not every module exposed by the much
larger Open-Xchange platform. The actions below are intentionally absent because the
mailbox.org deployment does not provide a functional backing endpoint.

## Folder subscribe/unsubscribe - unavailable on this deployment

Live probes against a temporary mail subfolder showed:

- `/folders?action=unsubscribe` returns `SVL-0001` (unknown AJAX action);
- `/folders?action=subscribe` returns `FLD-1001` ("Subscribe not supported for known
  trees") for the only available mail tree;
- putting `subscribed:false` through `folders?action=update` returns success but is a
  silent no-op when read back.

Registering commands that always error or report a state change that never happened
would be misleading, so `folders.subscribe` and `folders.unsubscribe` are not exposed.

## msisdn - out of scope (category mismatch, not a gap)

`msisdn` is not a CRUD data resource on this deployment; it is a mobile-number-based
login/authentication mechanism. It has no list/get-able data surface, so it does not
fit this CLI's read/write/destructive operation model and nothing was implemented
for it.

Live evidence (test account, `office.mailbox.org/ajax`):

- `GET /ajax/msisdn?action=<all|list|get|info|action>&session=<id>` returned a bare
  HTTP 404 for every action tried, byte-identical to the 404 returned for a
  deliberately nonexistent module path (`/ajax/foobarmodule?action=all`) used as a
  control. This is a routing-level 404, unlike every other "action not supported"
  case in this API, which returns HTTP 200 with an OX error envelope (e.g.
  `{"code":"SVL-0001","error_desc":"Unknown AJAX action."}`). That means the
  `msisdn` module itself is not mounted on this deployment, not merely missing one
  action.
- `GET /ajax/login?action=msisdn&session=<id>` returned `200` with
  `{"code":"SVL-0001","error_desc":"Unknown AJAX action."}` - recognized as a
  request to the `login` module, but `msisdn` is not a valid action there either.

Conclusion: `msisdn` is absent on this deployment both as a data module and as a
login action. Where it exists elsewhere in the OX ecosystem, it authenticates a
session via a phone number - it does not list, get, create, or delete anything
meaningful. Implementing a fake `mbx msisdn` command with no real backing data
would violate this project's "don't ship a lie" precedent, so nothing was added.
