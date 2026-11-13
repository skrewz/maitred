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
`/var/lib/maitred/forgejoeng/`), keyed by
`repo + kind(issue|pr|forest) + number` (the forest kind is the
[Forest digest](#forest-digest) slot below).
Writes are **atomic**: a reader never observes a torn or partially written
watermark.

## Persistence

The watermark must **survive a maitred restart**: a store newly opened on
the same directory sees the watermarks a previous instance saved.

## Forest digest

A forest has **one slot per forest** in the same store — keyed by
`repo + kind(forest) + number`, where the repository and number name the
forest's **ideation root**, so the `decompose` decision made on that root
finds the slot (§forgejo/decisions/actionable-state-digest). The slot is
an ordinary watermark record whose **digest** field carries the forest's
last observed actionable-state digest; `action` and `revision` are unused
there, and the forest kind keeps the slot's file distinct from the root
issue's own watermark. Like watermarks, forest slots are written
**atomically**, serialised through the same **per-key lock**, and
survive a restart (§forgejo/state/storage, §forgejo/state/persistence,
§forgejo/state/concurrency). The slot exists so a digest gate can make a
quiet forest stay quiet across restarts: when it lands it must refresh
the slot on every evaluation, dispatched or skipped, so the next sweep
compares against the newest observed state.

The slot is **lossy** like the watermark: a lost slot simply makes the
next comparison see a digest that moved, and the cost is one redundant
(decompose) run (§forgejo/state/lossiness). The engine does **not yet**
write or read the slot: the digest gate that consults it and refreshes
it on every evaluation is separate work.

## Lossiness

It is acceptable for the watermark to be **lossy**: losing it only causes a
redundant, idempotent re-dispatch (the canned prompts keep their pre-flight
checks), never a correctness failure.

## Concurrency

Watermark updates for the same key are **serialised** (a per-key lock or
equivalent), so the event path and the reconciliation sweep cannot race a
watermark update for the same issue/PR.

Where one critical section reaches into another key the order is
**root first, target second**, and never the inverse. The engine's only
such nesting is the unblock cascade's legs — PR, sync and issue — all
reached while `HandleEvent` still holds the cascade root's key lock, and
the only holder allowed to take a second key's lock while holding one,
so a future leg that dispatched from inside another key's lock would be
out of bounds. Inverting the order would need one object to be a cascade
root in one delivery and a cascade target in another at the same
instant — a cycle in the blocker graph, which the forge's blocker check
rejects and which the walk's own visited set would break whatever the
forge allowed (§forgejo/decisions/unblock-cascade).
