# User and administration

Load this guide only for the current user, quota, capabilities, groups, resources, or
external mail accounts.

Use narrow reads first:

```sh
mbx whoami --format values --select email
mbx quota get --type filestore --llm
mbx capabilities list --llm
mbx group list --pattern <pattern> --format tsv --select id,display_name
mbx resources search --pattern <pattern> --format tsv --select id,display_name,email
mbx account list --format tsv --select id,name,primary_address
```

Groups, resources, and external accounts may require administrative capabilities. Never
delete built-in ids. Mutate only the exact object selected by the user or a uniquely
named fixture created in the same workflow.

External-account commands carry third-party credentials. Keep values out of transcripts
and confirm the server is trusted. Give the external password with `--password-stdin`
(first line of stdin) or `--password-env` (reads `MBX_ACCOUNT_PASSWORD`). `--password`
still works, but it shows the password in the process list and prints a warning. For profile updates, fetch current state and preserve
fields the user did not request to change.
