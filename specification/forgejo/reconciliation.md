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
actions below, whose watermark is the state. The sweep is safe to run
at any time, and re-running it is a no-op until Forgejo's state or the
watermarks move.

## Watermark exemptions

Dependency edges emit no webhook delivery: attaching or removing a
blocker leaves the issue's `updated_at` untouched. For the actions
whose trigger is such a state relation, a revision-keyed watermark can
therefore suppress a dispatch the current state still calls for —
correct-and-silent when it needs to be correct-and-loud.

For a set of **watermark-exempt** actions, the watermark is the
**state**, not the revision: the sweep re-fires them whenever the
state calls for the action, revision notwithstanding. The engine's job
is only to stop suppressing them; the agent's own silence-on-noop is
what keeps the re-firing quiet (and the queue's deduplication squelches
a re-dispatch while the prior task is still pending).

| Exempt action | The state that calls for it (on reconcile) |
|---|---|
| `decompose` | the issue is open and carries the `ideation` label |
| `wrap-up` | the issue is open, carries the `outcome` label, and has no open blockers |

`ideation` and `outcome` are reserved label names, not configuration.
When an issue carries both, `decompose` wins: a root is decomposed
before it is wrapped up.

The exemption is scoped to the sweep. On the **event path** an issue
carrying either reserved label dispatches neither `implement` nor
`reassess`: the decision is a hold-off naming the label. A protected
issue is therefore never handed to the implementer on any path, however
the label arrived.

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
