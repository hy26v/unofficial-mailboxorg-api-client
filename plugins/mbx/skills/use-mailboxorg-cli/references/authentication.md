# Authentication and configuration

Load this guide only for login, logout, config, password, session, or authentication
failures.

- Put the non-secret username in user-level `~/.mbx/config.toml`, or let an explicit
  interactive `mbx login` ask for it and save it there with private permissions.
- For an explicit `mbx login` on an interactive terminal, let the CLI read the password
  through its hidden prompt.
- For non-interactive automation, supply `MBX_PASSWORD` only through the process
  environment; the CLI fails rather than prompting when stdin is not a terminal.
- Keep password values out of config, command transcripts, logs, and responses.
- Let `mbx` manage `~/.mbx/session.json`; treat it like a password.
- Commands after login reuse that session without needing `MBX_PASSWORD`; if it is no
  longer reusable, run `mbx login` again.
- The session works only for the account that created it. After a username change, or
  with a session file from an older `mbx`, run `mbx login` again.
- Use `mbx login` for a fresh login and `mbx logout` to invalidate and clear the session;
  after logout, another `mbx login` creates a new session normally.
- Do not loop on rejected credentials. Full logins are serialized across processes using
  one `MBX_HOME` and back off exponentially; provider cooldowns remain in
  `request-guard.json` even when a command exits.

`MBX_HOME` may relocate config and state to an absolute directory for user-authorized
portable automation. Never change it to avoid the user's safety configuration.

If setup is missing in a non-interactive workflow, name the required config key or
environment variable without requesting the secret in chat. Use `mbx help config` or
`mbx help auth` for local guidance.

Treat `change-password` as sensitive. On a terminal it asks for the current password
and the new password (twice) with hidden input. In automation, set `MBX_PASSWORD` (the
current password) and `MBX_NEW_PASSWORD` in the process environment. Do not use
`--current-password` or `--new-password`: other local users and shell history can see
command arguments, and `mbx` prints a warning when you use them. Inspect the exact
operation and apply the mutation guide before using it.
