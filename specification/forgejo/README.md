# Forgejo engine

Specification home for the Forgejo engine domain: a stateful webhook component
that tracks issue/PR state across the organisation's `maitred-enabled`
repositories and dispatches canned hotelier tasks when a tracked transition
warrants it.

This folder is created by the spec-driven scaffolding change (issue #47).
Per-change specifications land here — in the same change that implements them —
as the Forgejo engine chain (issues #48–#54, end-state #46) is implemented.

Specifications that have landed:

| File | Reference |
|---|---|
| `client.md` | the read-only Forgejo API client — §forgejo/client/operations |
