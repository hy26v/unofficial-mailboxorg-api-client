# Folders

Load this guide only for folder discovery, creation, renaming, permissions, or deletion.

Start with the narrowest applicable operation:

```sh
mbx help folders
mbx describe folders.list --llm
mbx describe folders.get --llm
```

List only the relevant subtree and preserve folder ids exactly. Before creating a
folder, inspect its intended parent. Before updating, fetch the folder and preserve any
permissions the user did not ask to replace.

To create a primary-account mail folder alongside other top-level mail folders, use the
private-tree root shown by `folders.list`:

```sh
mbx folders create --parent 1 --module mail --title "Projects" --dry-run --llm
mbx folders create --parent 1 --module mail --title "Projects" --preflight --llm
mbx folders create --parent 1 --module mail --title "Projects" --llm
```

The preview shows both `requested_parent: "1"` and the normalized provider parent
`default0`; live execution uses that exact plan. For repeatable setup, prefer the
idempotent ensure operation, which returns the existing exact child or creates and
verifies it once:

```sh
mbx folders ensure --parent 1 --module mail --title "Projects" --preflight --llm
mbx folders ensure --parent 1 --module mail --title "Projects" --llm
```

For a nested folder, pass the exact parent folder id returned by `folders.list` instead.
If ensure creates the folder but cannot verify it, the `verification_failed` error message
names the created folder id. Inspect that folder before you retry.

`folders update` and `folders delete` read the folder first and send its UTC modification
timestamp, so the server refuses the change if the folder changed in between. Mail
folders have no such timestamp, so the CLI sends none for them.

Treat built-in and default folders as user data, not test fixtures. Never delete one.
For a requested deletion, inspect the folder immediately beforehand, confirm the exact
id required by `folders.delete`, and verify that the target is the user-selected folder.
