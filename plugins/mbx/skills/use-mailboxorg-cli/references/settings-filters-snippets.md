# Settings, mail filters, and snippets

Load this guide only for settings trees, mail-filter rules, or signature snippets.

Read the narrowest settings key possible:

```sh
mbx settings get --path io.ox/mail --key <top-level-key> --llm
```

Before `settings.set`, capture the old value. Change exactly the requested top-level
key, verify it, and retain the old value for rollback when testing. `settings.set`
merges one key into the tree (jslob `action=update`). An object value replaces the old
object at that key (jslob `{"path","value"}` form), so its old subkeys are removed; the
key must not contain `/`. After the write it reads the tree
again. It fails if the key does not have the new value, or if another top-level key
changed or is missing; then read the tree and restore the lost keys.

For mail filters, list before selecting a rule. Create test filters inactive, keep them
inactive during updates, and never enable a rule that could redirect, delete, or alter
real mail.

Snippets are mail signatures. List and inspect the exact snippet before update or
delete; never overwrite an existing signature as a test fixture.
