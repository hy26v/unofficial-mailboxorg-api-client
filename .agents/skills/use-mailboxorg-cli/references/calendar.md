# Calendar

Load this guide only for calendar events and iCalendar import/export.

Event lists are always bounded by time. Without `--from` the range starts now, and
without `--to` it ends 30 days after the start; give both for another range:

```sh
mbx events list --format tsv --select id,summary,startDate,endDate
mbx events list --from <rfc3339> --to <rfc3339> --limit 50 \
  --format tsv --select id,summary,startDate,endDate
```

Create events with an end later than the start, then verify by the returned id and
folder:

```sh
mbx events create --summary <summary> --start <rfc3339> --end <rfc3339> \
  --dry-run --llm
mbx events get --id <id> --folder <folder> --llm
```

`events list` returns each occurrence of a recurring series as its own item, with the
series `id` and its `recurrenceId`. Add `--no-expand` to get each series once. The human
table has a `recurrence_id` column (empty for a single event) and shows a time with its
zone, for example `20260708T110000 Europe/Berlin`, unless the zone is UTC.

Times are RFC3339 values. `events create` stores them in `--timezone <IANA name>` if
given, else in the account's configured time zone, else in UTC. `events update` keeps the
event's current time zone unless `--timezone` is given. For an all-day event, update uses
only the date of `--start` and `--end` and keeps both as dates.

To change or delete one occurrence, add `--recurrence-id <recurrenceId>` to
`events update` or `events delete`, with the series id as `--id`. This also works for an
occurrence that was changed before and so has its own exception id. To delete a whole recurring series, `events delete`
requires `--series`; without it the command refuses and changes nothing.

Fetch the latest timestamp before update or delete operations. Use
`events.export --output <file>` for raw iCalendar data and inspect
`events.import` before importing a file; do not route iCalendar text through structured
output formatting.

An import file can hold many objects. The import output always lists every stored object
in `items` and every failed entry in `failed` (`index`, `code`, `message`). `index` is the
0-based entry position. If one or more entries fail, stdout still has this full report,
stderr has an `import_incomplete` error, and the exit code is `1`. Use `items` to verify or
remove the objects that were stored. One entry can be in both lists: the server stored it
but also returned an error for it, or stored it in a different folder.
