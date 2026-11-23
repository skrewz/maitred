# Forgejo state

The engine persists a small **"what have I dispatched" watermark** per
issue/PR, and one digest slot per forest in the same store
([Forest digest](#forest-digest)).

## Watermark

A watermark records, for one issue or pull request:

- the last dispatched **action** (the named action from the decision
  function), and
- the **revision** it was dispatched at, which is that action's
  **declared digest** (§forgejo/decisions/actions): the digest of the input
  set the action's declaration names, so the question the row asks and the
  value its guard compares are derived from one place. For an issue-path
  action that digest is a hex SHA-256 over the state facts the declaration
  names — the issue's state and labels, its body outside the region the
  engine owns, the state of each of its blockers, the pull requests
  connected to it, the downstream edges a teardown order reads, the forest
  digest the `decompose` gate observed — folded as a **set**, so the same
  state yields a byte-identical digest whatever order the listings arrive in
  and any fact a predicate reads moves it
  (§forgejo/decisions/actionable-state-digest,
  §forgejo/reconciliation/the-tracker-block, s-git/maitred#259). No event
  fact folds into it: which row fired is the event's business, and a digest
  the sweep and an event must answer with one value cannot contain a fact
  only one of them carries. No issue timestamp folds in whole either — the
  `updated_at` of an `outcome` tracker moves when the engine rewrites its
  current-state region, and a fact the engine moves by writing it is a fact
  no guard can pace, so a tracker folds the facts its masked revision folds
  instead (§forgejo/reconciliation/the-tracker-block,
  §forgejo/decisions/loop-prevention). The pull-request actions and the
  landing set still name the object revision their rows were keyed on — the
  PR head sha, and the revision derived from the whole set — and re-keying
  them is the next member of this chain (s-git/maitred#260).

The digest is the one value the idempotency guard compares, the re-fire
bound is a rate on, and the task's dedup key carries; the decision log
names it for a dispatch, and names the object's own revision for a
hold-off, which reaches no declaration to derive a digest from
(§forgejo/webhook/the-pipeline, §forgejo/observability/the-decision-log).
Re-keying an action is an edit to its declaration and to nothing beside it.

The revision records *what the engine last reacted to*, never whether an
object is actionable: eligibility is a predicate over re-fetched state
alone (§forgejo/reconciliation/the-state-is-the-watermark). A digest the
watermark does not record is new information and dispatches; one it does
record means the state has not moved since the engine last acted on it,
and the row dispatches anyway — no action may fall silent at a digest it
cannot move — leaving the pace of that re-fire to the re-fire bound
(§forgejo/decisions/loop-prevention). This is the same on both paths, which
is the point of keying on a digest rather than a timestamp: the sweep and
the event ask one question of one state and get one answer
(§forgejo/reconciliation/the-state-is-the-watermark, s-git/maitred#259).

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
last observed actionable-state digest and whose **digest_parts** field
carries the same fold's sub-digest per component — root, member, tracker,
blocker and comment — so the gate that finds a moved digest can name
which component moved (§forgejo/decisions/actionable-state-digest,
s-git/maitred#209). `action` and `revision` are unused
there, and the forest kind keeps the slot's file distinct from the root
issue's own watermark. Like watermarks, forest slots are written
**atomically**, serialised through the same **per-key lock**, and
survive a restart (§forgejo/state/storage, §forgejo/state/persistence,
§forgejo/state/concurrency). The gate reads the slot on the paths whose
rows dispatch `decompose`; a newer observation is written to the slot
once the dispatch it justified has landed, a digest that has not moved
writes nothing — the stored slot already holds it — and a dispatch that
failed or was refused leaves the stale slot so the next sweep retries
(s-git/maitred#209).

The slot is **lossy** like the watermark: a lost slot simply makes the
next comparison see a digest that moved, and the cost is one redundant
(decompose) run (§forgejo/state/lossiness). A slot whose parts are
absent — written by a build predating them, say — reads as a forest
whose digest moved but whose components cannot be localised, so the
gate dispatches with its reason naming no component beyond the digest
itself.

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
