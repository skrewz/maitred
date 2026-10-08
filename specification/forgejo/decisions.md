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

The engine's own scope is tested **ahead** of this function, in the
pipeline (§forgejo/webhook/repository-roles, §forgejo/webhook/the-scope-cache):
an event whose repository holds no maitred role holds off with the
reason `repo <name> holds no maitred role (neither maitred-enabled nor
maitred-outcomes-repo)` and never reaches `Decide`;
whether an action is *admissible* in the roles of the repository holding
the object is applied by the pipeline after `Decide`
(§forgejo/webhook/the-pipeline). Either way the function stays pure —
repository roles are not among its inputs. When
the engine cannot establish scope because the repository listing failed,
the hold-off reason instead names the failure (`scope of repo <name>
unknown: ...`), keeping exclusion and API failure distinguishable in the
decision log.

## Actions

The action names form the enum the engine config maps to canned
prompts:

| Action | Dispatched when |
|---|---|
| `implement` | the issue is open, unblocked (all its blockers closed), and has no connected open PR — the issue was opened or became unblocked |
| `reassess` | an open issue was edited, had its labels changed, or received a feedback comment (the prompt decides whether to implement) |
| `review` | a mergeable PR opened, or synced while it has no review |
| `re-review` | a mergeable PR synced while it has a review |
| `fix-feedback` | the PR's latest review requests changes, or a reconciled PR stands at a comment review |
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
| reconcile — the sweep's synthetic current-state event | as issue opened (issue); for a PR, as PR synced — except when the last activity is a review: changes-requested or comment dispatches `fix-feedback`, approved dispatches `merge-or-wait` |

The "latest review" is the PR's most recently submitted review. An
issue that *became unblocked* is dispatched `implement` through the
unblock cascade, under the same conditions as an opened issue.

**Reserved labels.** An issue carrying the `ideation`, `outcome`, or
`human-task` label is **never** an `implement` or `reassess` candidate:
the issue-opened, reconcile, and issue-activity decisions hold off on it
whatever the repository's roles — a mislabelled outcomes ticket, or one
in a repository holding both roles, must not wake the implementer. The
`human-task` label marks a ticket claimed by a human: it is never an
`implement` candidate anywhere in the engine, the unblock cascade
included (§forgejo/decisions/unblock-cascade). The role model
(§forgejo/webhook/repository-roles) is the load-bearing boundary; this
rule is insurance.

A PR's **last activity is a review** when its latest review is a
changes-requested, approved, or comment review submitted against the PR's
current head (the review's commit ID equals the head SHA) — no commit has
been pushed since the review. A reconciled PR standing at such a review
decides as if that review had just been submitted — except that a comment
review, which holds off on the event path, dispatches `fix-feedback`: the
review is feedback for the implementer, and the sweep moves the PR along by
triggering the implementer, not the reviewer. Changes-requested dispatches
`fix-feedback`, approved dispatches `merge-or-wait`. A PR whose latest
review is anything else (e.g. dismissed), or whose latest review predates
the current head, is reconciled as PR synced.

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

The cascade dispatches `implement`: an issue newly unblocked but
carrying a reserved label (`ideation`, `outcome`, `human-task` — see
§forgejo/decisions/transition-table) is held off, naming the label,
instead of dispatched. Each cascaded key's admissibility is judged by
the roles of the repository **holding the key**, never the repository
whose closure rooted the cascade (§forgejo/webhook/repository-roles).

The walk itself is label-blind: labelled nodes are visited and
traversed, so graph walking stays independent of what is finally
dispatched.

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
