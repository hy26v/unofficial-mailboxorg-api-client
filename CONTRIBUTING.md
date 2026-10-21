# Contributing

Bug reports, ideas and pull requests are welcome.

## How releases work

Development happens in a separate repository. This public repository receives one
squashed commit per release, and each release commit is tagged (`v0.1.0`, `v0.2.0`, and
so on). Released commits and tags are never rewritten.

Because of this, a pull request is not merged directly. If the change is accepted, the
maintainer applies it in the development repository. It is published in the next release
commit, with a `Co-authored-by:` trailer that credits you. The pull request is then
closed with a link to that release.

## Before you open a pull request

- Open an issue first for larger changes, so we can agree on the approach.
- Keep each pull request to one change.
- Run the offline checks. They use synthetic data and local fake servers, and need no
  mailbox.org account:

  ```sh
  gofmt -l .
  go vet ./...
  go test ./...
  go test -race ./...
  ```

- Add or update tests for any change in behavior.
- Do not weaken the request guard, delete protection, read-only mode or confirmation
  checks. Changes to them need a test that shows the protection still holds.

## Agent skill

The canonical skill is
[`.agents/skills/use-mailboxorg-cli/SKILL.md`](.agents/skills/use-mailboxorg-cli/SKILL.md).
Codex finds it in `.agents/skills`, and Claude Code finds it through the
`.claude/skills/use-mailboxorg-cli` symlink. The Claude Code plugin in `plugins/mbx` keeps
a copy, because plugins install real files only. After you change the skill, run
`scripts/sync-plugin-skill.sh`; a test fails while the copy differs.

## MCP agent evaluation

To measure how many MCP tool calls an agent needs for common tasks, run the evaluation
against a fake mailbox; see [MCP agent evaluation](docs/MCP_EVAL.md).

## Live tests

Live tests run against a real mailbox.org account that you supply. Use a dedicated test
account, never your personal mailbox. See [Live testing](docs/LIVE_TESTING.md). Do not put
credentials, session files or real message content in issues, pull requests or tests.

The live runner defaults to non-mutating coverage. The full matrix is explicitly gated,
creates uniquely named fixtures, sends one message only to the test account itself, and
cleans up in reverse dependency order:

```sh
# Supply MBX_USERNAME and MBX_PASSWORD through the environment first.
./scripts/live-test.sh safe
MBX_LIVE_WRITES=1 ./scripts/live-test.sh full
```

Add `MBX_LIVE_PASSWORD_ROUNDTRIP=1` only on a dedicated account to include the
reversible password-change endpoint. Without it, the runner reports that one operation
as `SKIPPED`.

## License

By contributing, you agree that your contribution is licensed under the
[MIT License](LICENSE).
