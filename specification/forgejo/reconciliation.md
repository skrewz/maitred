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
   as pull requests — the pull request list covers them.
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
the remaining repositories. Per-object failures follow the event path's
hold-off semantics (§forgejo/webhook/the-pipeline). A sweep never
aborts the engine.

## Idempotency

Because the sweep feeds the same decision function the same watermark,
an already-dispatched `(action, revision)` is not re-dispatched
(§forgejo/decisions/loop-prevention) — except for the watermark-exempt
actions below, whose rows on the sweep decide without consulting the
watermark. The sweep is safe to run at any time, and re-running it is a
no-op until Forgejo's state or the watermarks move.

## Watermark exemptions

Dependency edges emit no webhook delivery: attaching or removing a
blocker leaves the issue's `updated_at` untouched. For the actions
whose trigger is such a state relation, a revision-keyed watermark can
therefore suppress a dispatch the current state still calls for —
correct-and-silent when it needs to be correct-and-loud.

For a set of **watermark-exempt** actions, the watermark is the
**state**, not the revision: the sweep re-fires them whenever the
state calls for the action, revision notwithstanding. The exemption is
a property of the **sweep's rows** for those actions, not of the
idempotency guard, which stays revision-keyed on every path
(§forgejo/decisions/loop-prevention). The engine's job is only to stop
suppressing them; the agent's own silence-on-noop is what keeps the
re-firing quiet (and the queue's deduplication squelches a re-dispatch
while the prior task is still pending).

| Exempt action | The state that calls for it (on reconcile) |
|---|---|
| `decompose` | the issue is open and carries the `ideation` label, or it is open, carries the `outcome` label, has an open blocker, and the `ideation` issue it is blocked by is closed |
| `wrap-up` | the issue is open, carries the `outcome` label, and has no open blockers |
| `teardown-forest` | the issue is open, carries the `ideation` label, carries the `teardown-forest` label **and blocks at least one issue** — the forest still standing is the state that calls for the deletion, so a teardown that died half way through re-fires until the graph is empty, and stops the sweep the moment it is (§forgejo/decisions/forest-teardown) |

`ideation` and `outcome` are reserved label names, not configuration.
They match **case-insensitively**: a label name is a user-authored
string, and a label created as `Ideation` must not silently decide as
`implement`. When an issue carries both, `decompose` wins: a root is
decomposed before it is wrapped up.

The exemption is scoped to the sweep. An issue carrying either reserved
label dispatches neither `implement` nor `reassess` on any path — a
protected issue is therefore never handed to the implementer, however
the label arrived. For `decompose` the event path has rows of its own:
opening, editing, or changing the labels of an open `ideation` issue
dispatches `decompose` like the sweep row does, but under the ordinary
revision-keyed watermark guard — the exemption, not the action, is what
belongs to the sweep (§forgejo/decisions/transition-table). A comment
on an open root holds off: it is not a revision of intent. For
`wrap-up` the sweep stays the only exempt firing row: an `outcome`
tracker's activity on the event path is a hold-off naming the label.
The **unblock cascade** dispatches `wrap-up` too, when it newly
unblocks an `outcome` tracker (§forgejo/decisions/unblock-cascade); a
cascade dispatch is not exempt — it applies the ordinary revision-keyed
guard for its own key, and the sweep stays the state-based backstop
that re-fires the wrap-up at an unchanged revision when, say, a human
deleted an edge and no delivery fired.

The sweep's `sync` row is **not exempt**: a landing set's watermark is
a revision **derived from the whole set** — per member, the state the
sync predicate reads: its own state, the member label's presence, and
its delivering PR's head sha, open and mergeable state, and approval
at that head — so the sweep does not re-fire at an unchanged set
state, and any member's readiness moving re-fires. The members'
`updated_at` is not part of the digest, so editing a member without
moving its readiness does not re-run the landing prompt
(§forgejo/decisions/sync-set). The row is the backstop for set changes
that carry no delivery — an edge edit, a human closing a member: the
candidate not fired by any event is caught within one reconcile
interval.

All other actions — `implement`, `reassess`, `review`, `re-review`,
`rebase`, `fix-feedback`, `merge-or-wait` — keep revision-keyed
watermarks: re-dispatching those would re-run real work.

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
