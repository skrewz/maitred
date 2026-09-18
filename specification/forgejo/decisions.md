# Forgejo decisions

The heart of the engine: a **pure** decision function that maps a
Forgejo event, the re-fetched state, and the watermark to a decision.
No I/O — the function never touches the network or the disk; its
inputs are the re-fetched state (Forgejo is the source of truth) and
the watermark (§forgejo/state/watermark).

## The decision function

`Decide(event, state, watermark) -> Decision`.

- **Event** — the notification: its type, the affected object (repo,
  kind, number), and the sender's login.
- **State** — the re-fetched authoritative state of the affected
  object, plus the blocker-graph data the unblock cascade needs.
- **Watermark** — the last dispatch recorded for the object's key, or
  none.

A **Decision** is either a **dispatch** — a named action for the
event's own object — or a **hold-off**; both carry a human-readable
**reason** (it is logged with every decision). A close event's
decision additionally carries the **unblock cascade**: the issues now
unblocked, each warranting a dispatch of `implement`.

When the event and the re-fetched state disagree (out-of-order or
stale delivery), the **state wins**: a transition whose preconditions
the state does not satisfy holds off.

## Actions

The action names form the enum the engine config maps to canned
prompts:

| Action | Dispatched when |
|---|---|
| `implement` | the issue is open, unblocked (all its blockers closed), and has no connected open PR — the issue was opened or became unblocked |
| `reassess` | an open issue was edited, had its labels changed, or received a feedback comment (the prompt decides whether to implement) |
| `review` | a mergeable PR opened, or synced while it has no review |
| `re-review` | a mergeable PR synced while it has a review |
| `fix-feedback` | the PR's latest review requests changes |
| `merge-or-wait` | the PR's latest review approves (the merge policy is the prompt's) |
| `rebase` | an open PR is not mergeable (conflicts) |

## Transition table

| When (event + re-fetched state) | Decision |
|---|---|
| issue opened (open, all blockers closed, no connected PR) | `implement` |
| issue opened (blocked, or a connected open PR, or not open) | hold off |
| issue edited / labels changed / feedback comment (issue open) | `reassess` |
| issue edited / labels changed / feedback comment (issue not open) | hold off |
| issue closed | unblock cascade |
| PR opened (open, mergeable) | `review` |
| PR opened (open, not mergeable) | `rebase` |
| PR synced (open, not mergeable) | `rebase` |
| PR synced (open, mergeable, no review) | `review` |
| PR synced (open, mergeable, has a review) | `re-review` |
| review submitted (latest review requests changes) | `fix-feedback` |
| review submitted (latest review approves) | `merge-or-wait` |
| review submitted (latest review is anything else) | hold off |
| PR merged | unblock cascade (for the connected issue) |
| PR closed without merging | hold off |
| reconcile — the sweep's synthetic current-state event | as issue opened (issue) / PR synced (PR) |

The "latest review" is the PR's most recently submitted review. An
issue that *became unblocked* is dispatched `implement` through the
unblock cascade, under the same conditions as an opened issue.

## Unblock cascade

On closure — an issue closed, or a PR merged whose connected issue
auto-closes — the decision follows the blocker graph from the closed
issue, including **cross-repo** blockers (each node carries its own
repository), and adds one `implement` dispatch per issue that is now
unblocked: open, with **all** its blockers closed.

The cascade is **transitive**: a blocked issue that is itself closed
cascades onward to the issues it blocks (A→B→C). It **terminates**
without double-dispatching: each issue is visited once. The engine
applies each cascaded key's own watermark before dispatching, so an
already-dispatched `(action, revision)` is not re-dispatched.

## Loop prevention

Three mechanisms, in this order:

1. **The machine is terminating.** Each action's side-effect maps to
   the *next* transition, never a re-run of the same one: a review
   does not push commits; a commit push triggers a review; a
   changes-requested review triggers a fix; a fix pushes commits.
2. **Idempotency + watermark.** The same `(event, state, revision)`
   does not re-dispatch the same action: when the watermark already
   records the action at the current revision (the PR head sha, or the
   issue's `updated_at`), the decision holds off.
3. **Targeted per-transition sender guards**, only where an agent's
   *own* action would re-trigger the *same* action: a commit pushed by
   the PR's reviewing author (the author of its latest review) must
   not re-review that same commit. Cross-persona triggering (a
   reviewer's changes-requested leading to the implementer's fix) is
   intended and unguarded.

## Inputs

**Event** — type, the affected object (repo, kind, number), and the
sender's login. The event types are: issue opened / edited / labels
changed / commented / closed; PR opened / synced / reviewed / merged /
closed; and the synthetic `reconcile` event of the reconciliation
sweep.

**State** — the re-fetched state of the affected object: the issue or
PR itself; the issue's blockers (with their states) and its connected
open PRs, for the `implement` conditions; the PR's reviews, for the
review transitions; and, for close events, the blocker-graph root —
the closed issue — with each node carrying the issues it blocks and
the issues that block it.

**Watermark** — the last dispatch for the object's key
(§forgejo/state/watermark), or none.
