# Tasks and reminders

Load this guide only for tasks or reminders.

When `--folder` is omitted, the CLI uses the standard tasks folder from the account
configuration (`/config/folder/tasks`):

```sh
mbx tasks list --limit 50 \
  --format tsv --select id,title,status,end_date
mbx tasks get --id <id> --folder <folder> --llm
```

Run `mbx commands tasks` to discover all task operations.

Updates and deletes require the latest task timestamp. Re-read after every write before
the next optimistic-locking operation.

A reminder is backed by a task with an alarm:

```sh
mbx reminder create --folder <tasks-folder> --title <title> --at <rfc3339> \
  --dry-run --llm
mbx reminder list --from <rfc3339> --to <rfc3339> --limit 50 --llm
```

Without `--from` and `--to`, `reminder list` shows the next 30 days.

`reminder create` returns `id` (the reminder id that `reminder delete` takes) and
`task_id` (the backing task). If the task is created but its reminder cannot be found,
the output still has `task_id`, `id` is `null`, and the command fails with
`reminder_not_found`. Do not run the command again. Remove that task
with `tasks delete` if you do not need it.

The API returns every reminder due before `--to`. The CLI removes reminders due before
`--from`, then applies `--limit`.

When removing a reminder created for testing, verify the corresponding task identity so
an unrelated task is not deleted.
