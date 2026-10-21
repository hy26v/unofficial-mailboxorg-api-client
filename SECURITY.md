# Security policy

`mbx` handles mailbox.org passwords and session cookies. Please report a vulnerability
privately; do not open a public issue for it.

## Report a vulnerability

Use GitHub's private vulnerability reporting: open the repository's **Security** tab and
select **Report a vulnerability**. Include:

- the output of `mbx version`;
- the operating system;
- the steps to reproduce the problem;
- the effect, for example a credential or session leak, or a guard bypass.

Do not include real passwords, session files or message content. Use synthetic values.

Fixes are published in the next release, and the advisory credits you unless you ask
otherwise.

## Supported versions

Only the latest release gets security fixes.

## Scope

In scope: credential and session handling, the request guard, delete protection,
read-only mode, confirmation checks, file permissions and output that could expose
secrets or message content.

Out of scope: problems in mailbox.org or Open-Xchange servers. Report those to
mailbox.org.
