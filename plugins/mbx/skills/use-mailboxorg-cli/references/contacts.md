# Contacts

Load this guide only for contacts and vCard import/export.

Find the smallest useful record:

```sh
mbx contacts list --limit 50 \
  --format tsv --select id,display_name,email
mbx contacts get --id <id> --folder <folder> --llm
```

Create and update accept repeatable `--field name=value` and optional structured
`--input`. Fetch the current folder and optimistic-lock timestamp before updating or
deleting. For `--timestamp`, use `last_modified` from `contacts get`, or `timestamp` from
the last `contacts create` or `contacts update`. `contacts update` cannot move a contact:
it refuses a `folder_id` field that is not equal to `--folder`.

```sh
mbx contacts create --field display_name='Example' \
  --field email1='example@example.test' --dry-run --llm
mbx contacts update --id <id> --folder <folder> --timestamp <timestamp> \
  --field display_name='Updated' --dry-run --llm
```

Use raw files for vCard operations:

```sh
mbx contacts export --folder <folder> --output contacts.vcf --llm
mbx describe contacts.import --llm
```

Do not send raw vCard through JSON/TSV output formatting.

An import file can hold many objects. The import output always lists every stored object
in `items` and every failed entry in `failed` (`index`, `code`, `message`). `index` is the
0-based entry position. If one or more entries fail, stdout still has this full report,
stderr has an `import_incomplete` error, and the exit code is `1`. Use `items` to verify or
remove the objects that were stored. One entry can be in both lists: the server stored it
but also returned an error for it, or stored it in a different folder.
