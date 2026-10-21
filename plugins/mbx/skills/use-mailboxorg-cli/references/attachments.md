# Attachments

Load this guide only for attachments on contacts, tasks, or events.

Use the parent object's exact id and folder. Module codes are:

- `7` for contacts
- `4` for tasks
- `1` for events

Inspect the specific operation, then use the returned attachment id unchanged:

```sh
mbx attachments create --module <module> --object-id <parent-id> \
  --folder <folder> --file <path> --dry-run --llm
mbx attachments list --module <module> --object-id <parent-id> \
  --folder <folder> --llm
mbx attachments get --module <module> --object-id <parent-id> \
  --folder <folder> --id <attachment-id> --save-to <path> --llm
```

Keep the parent object until any attachment fixtures have been removed. Verify parent
identity immediately before attachment deletion.
