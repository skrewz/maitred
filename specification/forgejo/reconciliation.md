# Forgejo reconciliation

The engine does **not** detect missed webhooks directly. It
**reconciles** periodically against Forgejo and re-derives from the
source of truth (eventual consistency): a missed event shows up as a
stale watermark relative to Forgejo's current state, and the sweep
dispatches the delta. The sweep also catches issues and pull requests
created while the engine was down.

## The sweep

`Reconcile()` on the engine:

1. **Enumerate the repositories.** List the org's repositories
   (§forgejo/config/canned-prompts names the org) and keep those holding
   at least one role — repositories whose topics include
   `maitred-enabled` **or** `maitred-outcomes-repo`
   (§forgejo/webhook/repository-roles). Visiting an outcomes-only
   repository is what makes a missed delivery there self-heal like
   anywhere else; what may be dispatched for its objects is gated by
   admissibility (§forgejo/webhook/repository-roles). The same listing
   refreshes the event path's **scope cache** — the snapshot of which
   roles each repository holds (§forgejo/webhook/the-scope-cache) — so the
   sweep adds no API call to the scope test, and a repository excluded
   here is held off on the event path too.
2. **Enumerate the objects.** For each such repository, list **all**
   open issues and all open pull requests
   (§forgejo/client/operations). The issue list includes pull requests
   (Forgejo represents them as issues); the sweep skips the ones marked
   as pull requests — the pull request list covers them. The repository
   listing of step 1 reports each repository's units, so the sweep knows
   — **before** asking — whether a repository has the pull requests
   unit; where the unit is **off** the sweep does not list its pull
   requests at all: such a repository refuses the endpoint with a `404`
   and can hold no pull request, so asking would turn an ordinary state
   into an error line and no pull-request decision can come of it
   (§forgejo/client/operations). Issues are still listed and decided as
   usual.
3. **Decide and dispatch.** For each listed object, feed the synthetic
   `reconcile` event (§forgejo/decisions/inputs) through the **same
   pipeline as the event path** (§forgejo/webhook/the-pipeline):
   re-fetch, decide, dispatch, watermark — serialised on the watermark
   store's per-key lock (§forgejo/state/concurrency). The sweep shares
   the decision function and the event path; no logic is duplicated.

The synthetic event carries no sender.

## Scheduling

The sweep runs once when the engine starts, then on a **configurable
interval** — the `reconcile_interval` config value
(§forgejo/config/canned-prompts), positive, validated at load time.
Overlapping sweeps do not run concurrently: a sweep that starts while
another is in flight is skipped.

## Failure handling

A failure enumerating the org's repositories ends the sweep (it is
logged). A failure listing one repository's issues or pull requests is
logged, and the sweep continues with the repository's remaining list and
the remaining repositories. A repository whose pull requests unit is off
is **not** such a failure: its pull request listing is never attempted
(§forgejo/reconciliation/the-sweep), while a failing pull request
listing where the unit is on — a `404` from a renamed or deleted
repository included — stays an error (§forgejo/client/errors). Per-object
failures follow the event path's hold-off semantics
(§forgejo/webhook/the-pipeline). A sweep never aborts the engine.

## Idempotency

Because the sweep feeds the same decision function the same watermark,
an already-dispatched `(action, revision)` is not re-dispatched
(§forgejo/decisions/loop-prevention) — except for the watermark-exempt
actions below, whose rows decide without consulting the watermark:
the sweep's rows for all of them, and every path's row for
`merge-or-wait`. An exempt re-fire is bounded in time rather than by the
revision: the engine's **re-fire bound** lets one unchanged
`(action, revision)` dispatch at most once per `reconcile_interval`,
whichever path brought the event (§forgejo/decisions/loop-prevention).
The sweep is safe to run at any time, and re-running it is a
no-op until Forgejo's state or the watermarks move.

## Watermark exemptions

Dependency edges emit no webhook delivery: attaching or removing a
blocker leaves the issue's `updated_at` untouched. For the actions
whose trigger is such a state relation, a revision-keyed watermark can
therefore suppress a dispatch the current state still calls for —
correct-and-silent when it needs to be correct-and-loud.

For a set of **watermark-exempt** actions, the watermark is the
**state**, not the revision: the sweep re-fires them whenever the
state calls for the action, revision notwithstanding. For `decompose`,
`wrap-up`, `review`, `teardown-forest` and `fix-feedback` the
exemption is a property of the **sweep's rows** — the idempotency
guard stays revision-keyed on every other path for those actions
(§forgejo/decisions/loop-prevention). For `merge-or-wait` the
exemption is a property of **that action's rows on every path** — the
review event, a PR comment, the unblock cascade's pull-request leg and
the sweep alike — because the state it tests, every lens approving at
head, cannot be answered falsely mid-chain: an approved
`agentic-auto-merge` PR sits at a **frozen head** once the chain
approves, so a revision-keyed guard anywhere is a fixed point and a
merge run that died never recovers on an event. The `review` and
`fix-feedback` exemptions stay the sweep's rows for the opposite
reason: their state test, "this lens has not reported at head", is one
an event can answer falsely while the chain is still arriving
(§forgejo/decisions/review-lenses). The engine's job is only to stop
suppressing them; the agent's own silence-on-noop is what keeps the
re-firing quiet, the queue's deduplication squelches a re-dispatch only
while the prior task is still pending — never after it — and the
re-fire bound keeps one unchanged head to one dispatch per
`reconcile_interval` (§forgejo/webhook/the-pipeline,
§forgejo/decisions/loop-prevention).

| Exempt action | The state that calls for it (on reconcile) |
|---|---|
| `decompose` | the issue is open and carries the `ideation` label, or it is open, carries the `outcome` label, has an open blocker, and the `ideation` issue it is blocked by is closed |
| `wrap-up` | the issue is open, carries the `outcome` label, and has no open blockers |
| `teardown-forest` | the issue is open, carries the `ideation` label, carries the `teardown-forest` label **and blocks at least one issue that is not an `outcome` tracker** — a member the order would delete is the state that calls for the deletion, so a teardown that died half way through re-fires until nothing but a spared tracker remains, and stops the sweep the moment it does (§forgejo/decisions/forest-teardown) |
| `merge-or-wait` | the PR is open, **every configured review lens holds an undismissed approval at head**, no reviewer's latest undismissed review at head is a non-approval (§forgejo/decisions/review-lenses), no connected issue has an open blocker (the merge gate, §forgejo/decisions/merge-gate), and the work it delivers is opted into merging: the PR **or** the issue its body closes carries the `agentic-auto-merge` label (§forgejo/decisions/merge-gate) — the label is the whole point and a precondition of the dispatch on every path, not just this row: an approved PR with no opt-in on either seat waits on a human whatever the watermark records (§forgejo/decisions/merge-gate, s-git/maitred#179). And unlike the other exemptions this one is not the sweep's row alone: an approved, unblocked PR whose work is opted in re-fires on every path, its frozen head being the reason the revision cannot be the watermark (§forgejo/decisions/loop-prevention). |
| `review`, per lens | the PR is open, mergeable, and **this lens has reported nothing at the current head sha** — the report the sweep would ask for has still not happened, whether because no dispatch reached the lens or the head moved past its last one; a lens that has reported at head is exempt no more, its dispatch having landed (§forgejo/decisions/review-lenses) |
| `fix-feedback` | the PR is open, mergeable, **every lens has reported at the current head**, and some reviewer's latest undismissed review at head is not an approval — a comment report counts on this row, as feedback the sweep hands to the implementer — and the head has not moved since (§forgejo/decisions/review-lenses) |

`ideation` and `outcome` are reserved label names, not configuration.
They match **case-insensitively**: a label name is a user-authored
string, and a label created as `Ideation` must not silently decide as
`implement`. When an issue carries both, `decompose` wins: a root is
decomposed before it is wrapped up.

For the issue-side actions the exemption is scoped to the sweep
(`merge-or-wait` being the exception above). An issue carrying either reserved
label dispatches neither `implement` nor `reassess` on any path — a
protected issue is therefore never handed to the implementer, however
the label arrived. For `decompose` the event path has rows of its own:
opening, editing, or changing the labels of an open `ideation` issue
dispatches `decompose` like the sweep row does, but under the ordinary
revision-keyed watermark guard — the exemption, not the action, is what
belongs to the sweep (§forgejo/decisions/transition-table). A comment
on an open root takes the same event-path row under the same guard,
held off only when its author is the configured decomposer identity
(§forgejo/decisions/loop-prevention, s-git/maitred#147). For
`wrap-up` the sweep stays the only exempt firing row: an `outcome`
tracker's activity on the event path is a hold-off naming the label.
The **unblock cascade** dispatches `wrap-up` too, when it newly
unblocks an `outcome` tracker (§forgejo/decisions/unblock-cascade); a
cascade dispatch is not exempt — it applies the ordinary revision-keyed
guard for its own key, and the sweep stays the state-based backstop
that re-fires the wrap-up at an unchanged revision when, say, a human
deleted an edge and no delivery fired.

The sweep's `sync` row is **not exempt**: a landing set's watermark is
a revision **derived from the whole set** — the grouping issue's body,
and per member the state the sync predicate reads: its own
state, the member label's presence, and its delivering PR's head sha,
open and mergeable state, and approval at that head — so the sweep
does not re-fire at an unchanged set state, and any member's readiness
moving re-fires. The members' `updated_at` is not part of the digest,
so editing a member without moving its readiness does not re-run the
landing prompt; neither is the grouping issue's, which moves on a
comment or a label toggle as much as on a restatement. What the digest
does carry is the grouping issue's **body**: that is where the merge
order the landing run was told to honour lives, and a ready set whose
run reported an error is otherwise a fixed point the backstop cannot
fire on (§forgejo/decisions/sync-set). The row is the
backstop for set changes that carry no delivery — an edge edit, a human
closing a member, a human restating the order: the candidate not fired
by any event is caught within one reconcile interval.
by any event is caught within one reconcile interval.

All other actions — `implement`, `reassess`, `rebase`, and
`merge-or-wait` for a PR that carries no `agentic-auto-merge` opt-in —
keep revision-keyed watermarks on every path: re-dispatching those
would re-run real work. The `review` (per lens) and `fix-feedback`
exemptions are the sweep's rows alone; on the event path those actions
stay revision-keyed — the lenses through their subject slots
(§forgejo/state/watermark) — so a push or a review never re-fires on
its own (§forgejo/decisions/loop-prevention).

The `merge-or-wait` row is exempt for the reason `teardown-forest`
is, and not for the dependency-edge reason that opens this section: a
PR whose work carries the `agentic-auto-merge` opt-in — on the pull
request or on the issue its body closes, whichever seat the label sits in
(§forgejo/decisions/merge-gate) — sits at a **frozen head sha** once
approved — nothing more is pushed to it — so a revision-keyed watermark
makes it a fixed point no path can fire on again, and an opted-in PR
that was approved and never merged slips between the cracks and never
recovers. The state is therefore its watermark, **on every path**:
the row re-fires until the PR closes, and stops the moment it does —
the sweep because it enumerates **open** pull requests only
(§forgejo/reconciliation/the-sweep), the events because the merge gate
and the sync-set hold-off ahead of the row hold a gated PR at a
hold-off, and the **re-fire bound** because it lets one frozen head
dispatch at most once per `reconcile_interval` whichever event arrived
— the engine's own stamp, not the queue's `(action, revision)` dedup
key, which bounds a task's overlap and never the re-fire's spacing
(§forgejo/webhook/the-pipeline, §forgejo/decisions/loop-prevention).
The agent's silence-on-noop keeps a re-fire that found nothing to do
quiet (§forgejo/decisions/loop-prevention). Unlike the
`review` and `fix-feedback` rows — whose state test, "this lens has
not reported at head", an event can answer falsely while a chain is
still arriving, so their exemption reads the **event** and belongs to
the sweep alone — this row's state test, "every lens approves at
head", is as true on an event as on the sweep, so the exemption is a
property of the row and not of the path that reaches it.

A PR waiting to be reviewed sits at a **frozen head sha** — by
definition nobody is pushing to it while it waits — so a
revision-keyed watermark makes it a fixed point the sweep can never
fire on, and a review dispatch that went nowhere never recovers. The
state is therefore its watermark: the sweep re-fires until the state
changes, and every state in the table above is self-limiting — a
lens reporting at head ends that lens's `review` exemption and a
chain fully reported at head hands the PR to the review-following
rows (§forgejo/decisions/review-lenses,
§forgejo/decisions/transition-table), a push
ends `fix-feedback`, and the sweep enumerates **open** pull requests
only, so a merged or closed PR stops firing at once
(§forgejo/reconciliation/the-sweep). Recovery latency is one
`reconcile_interval`. Re-fires are bounded twice over: the queue's
`(action, revision)` dedup key squelches a re-dispatch only while an
identical task is still `PENDING` — a fact the queue reports for
pending tasks alone, which is what makes the exemption able to recover
a lost dispatch rather than be blocked by the dead one — and every
canned prompt's timeout is minutes against an interval measured in
hours, so a re-fire cannot overtake a task still running
(§forgejo/config/canned-prompts, §forgejo/webhook/the-pipeline).
Because these rows are reached from `decidePRSynced`, which the synced
event shares, the exemption keys on the **event**, not the shape of
the state, exactly as the `merge-or-wait` row does
(§forgejo/reconciliation/watermark-exemptions).

A hold-off for a watermark-exempt action always names the state fact
that calls for no action (e.g. `blocked by N open issue(s)`); the
`already dispatched at revision` reason belongs to revision-keyed
actions only, so the decision log distinguishes "already dispatched at
this revision" from "state calls for no action"
(§forgejo/observability/the-decision-log).

What the exemption buys is bounded latency: an edge change is
invisible until the next sweep, so a state-call for an exempt action
is acted on in **at most one reconcile interval**.

What it costs is steady-state noise, and the bound on that is stated
plainly: an exempt action re-fires on **every sweep while the state
holds** — an open `ideation` root, or an `outcome` tracker still
waiting to be closed, enqueues a task on each sweep for as long as the
label applies. Nothing in the engine bounds it further: the queue's
deduplication squelches a re-dispatch only while the prior task is
still in flight, and the agent's silence-on-noop carries the rest. That
is the design — the state *is* the watermark, and the engine's job is
only to stop suppressing these actions — so an operator weighing the
cost should read the re-firing as the mechanism working, not as a fault.

The two rows are not symmetric, and not by oversight: `wrap-up` waits
for the tracker's blockers to close, because wrapping up means
collecting finished work; `decompose` on a root consults no blockers,
because a root is a root whatever blocks it.

`decompose` has a second row, on the **tracker**, and it is what keeps a
forest alive after its commissioning (§forgejo/decisions/commissioning).
Closing the ideation issue is the commissioning gesture, and it also removes
the root from the sweep's enumeration of open issues: were the root the only
trigger, curation would end the moment the human commissioned the work — while
the work is exactly what reports the discoveries a forest must absorb (a
blocker nobody listed, a member that turned out to need another, a landing
set whose membership has moved). So an
open tracker that is still blocked is decomposed too, **provided its forest
is commissioned**: the `ideation` issue blocking it is closed. An open
tracker blocked by an **open** `ideation` issue holds off instead, naming
that root, because the root's own row is already dispatching the same curation
and a forest is curated once per sweep. A blocked tracker with no `ideation`
blocker at all holds off naming that absence: a tracker not gated by a root is
not a forest's tracker, and the engine does not guess which root it belongs to
(§forgejo/decisions/transition-table).

The test is one hop because the tracker's own blocker set is the forest, and
the tracker is itself a member: it carries the membership edge from its root
like every other member does. The engine reads that edge as the commissioning
gate; it never walks a graph to find one.
