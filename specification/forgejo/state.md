# Forgejo state

The engine persists a small **"what have I dispatched" watermark** per
issue/PR.

## Watermark

A watermark records, for one issue or pull request:

- the last dispatched **action** (the named action from the decision
  function), and
- the **revision/activity** it was dispatched at (e.g. the PR head sha, or
  the issue's `updated_at`).

Fan-out dispatches — the review lenses (§forgejo/decisions/review-lenses) —
give the record a **per-subject dimension**: alongside that main
`(action, revision)` slot sits one `(action, revision)` **slot per
subject**, keyed by the subject's identity (the lens's configured
login). The idempotency guard for a fan-out dispatch reads its own
subject's slot, so two lenses dispatched at the same revision do not
hold each other off, and a singleton dispatch's write keeps the
subject slots a previous fan-out left behind (§forgejo/state/storage).
Storage stays **one file per object**: the slots live in the same
record, and the per-key lock stays on the object.

## Storage

The watermark is stored as local JSON under the data directory (e.g.
`/var/lib/maitred/forgejoeng/`), keyed by `repo + kind(issue|pr) + number`.
Writes are **atomic**: a reader never observes a torn or partially written
watermark.

## Persistence

The watermark must **survive a maitred restart**: a store newly opened on
the same directory sees the watermarks a previous instance saved.

## Lossiness

It is acceptable for the watermark to be **lossy**: losing it only causes a
redundant, idempotent re-dispatch (the canned prompts keep their pre-flight
checks), never a correctness failure.

## Concurrency

Watermark updates for the same key are **serialised** (a per-key lock or
equivalent), so the event path and the reconciliation sweep cannot race a
watermark update for the same issue/PR.
