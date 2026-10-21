# Files and public shares

Load this guide only for infostore files or public share links.

Without `--folder`, `files list` and `files create` use the private infostore folder that
the server reports at run time (`GET /config/folder/infostore`); a dry run shows it as
`folder_id: null`. An empty or blank `--folder` is an `invalid_argument` error, not the
default folder. Use bounded listings to find the exact target, then preserve the
returned composite file id:

```sh
mbx files list --limit 50 --format tsv --select id,title,folder_id
mbx files get --id <composite-id> --llm
```

For a new file:

```sh
mbx files create --file <path> --title <title> --dry-run --llm
mbx files get --id <composite-id> --llm
```

Share operations take `--folder` plus `--id`. `--id` accepts the bare item id or the
composite `folder/id` from `files list`; a composite id must belong to `--folder`.
`share delete` first checks for a public link without creating one. If the file has no
link, it reports `not_found` and creates nothing. If another client removes the link
between that check and the delete, mbx deletes the new link that the API creates. If that
cleanup fails, the command fails with `share_link_left_behind`; the message names the
public link and the command that removes it.

Delete a public share before deleting its file. Never treat an existing user file as a
temporary fixture.
