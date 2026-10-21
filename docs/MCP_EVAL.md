# MCP agent evaluation

`scripts/mcp-eval` measures how many MCP tool calls an agent needs for common mailbox
tasks, and whether it completes them. It runs the real `mbx` MCP server against a fake
mailbox.org server, so it needs no account and changes no real data.

## What it does

- `serve` starts the fake mailbox (four messages, two calendar events, two contacts,
  four sender addresses) and serves the MCP server over stdio. The CLI inside the server
  sends its usual requests to the fake mailbox, which logs each one.
- `run` gives each task in [`tasks.json`](../scripts/mcp-eval/tasks.json) to
  `claude -p`, with only the `mbx` MCP server and no built-in tools. It counts the tool
  calls and the failed tool calls from the stream-json output, and checks the result:
  each check names a request that must (or must not) reach the fake mailbox, or text
  that the final answer must contain.
- Two server variants run: `task-tools` (the default `mbx mcp`) and `generic`
  (`--task-tools=false`, only the five general tools).

The write policy of the evaluation allows drafts and moves but no sending or deleting,
like a cautious owner configuration. One fixture message contains instructions for AI
assistants, so the checks also show whether a run sends mail or deletes anything.

## Run it

It needs Go and an authenticated `claude` CLI. Each run costs model usage; the default
model is `haiku`:

```sh
go run ./scripts/mcp-eval run                      # every task, both variants, once
go run ./scripts/mcp-eval run --repeat 3 --out results.json
go run ./scripts/mcp-eval run --only draft-from-alias --variants generic --model sonnet
```

The summary table lists the mean calls (and failed calls) and the successful runs per
task and variant. `go test ./scripts/mcp-eval` checks, without a model, that an ideal
call sequence passes every task, so the fixtures and checks keep working.

To compare with an older commit, build the harness in a worktree of that commit (copy
`scripts/mcp-eval` and remove the `TaskTools` line if the commit predates task tools)
and run only the `generic` variant there.

## Limits

- Model runs vary; use `--repeat` before you compare small differences.
- `claude -p` also reads your user-level `CLAUDE.md`, which can change the behavior.
- The fake mailbox answers a fixed set of requests; an agent that uses an operation
  outside the tasks gets `404` errors, which count as failed calls.
