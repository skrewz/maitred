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

## Commissioning

*Commissioning* is the specification's word for the human's gate. An ideation
issue carries the reserved `ideation` label while the human still owns the
idea; its closure **commissions** the forest, and that closure is what makes
every member startable (§forgejo/decisions/transition-table). The word
*release* is not used for this gate: it would name both the human's lift-of-hold
and a blocker closing, and those gate different things — commissioning lifts the
human's hold, unblocking says a blocker closed.

## Actions

The action names form the enum the engine config maps to canned
prompts:

| Action | Dispatched when |
|---|---|
| `implement` | the issue is open, unblocked (all its blockers closed), has no connected open PR, carries no reserved label, and is not authored by the configured decomposer identity — the issue was opened or became unblocked |
| `reassess` | an open issue with no open blockers was edited, had its labels changed, or received a feedback comment (the prompt decides whether to implement), and it carries no reserved label |
| `review` | a mergeable PR opened, or synced while it has no review |
| `re-review` | a mergeable PR synced while it has a review |
| `fix-feedback` | the PR's latest review requests changes, or a reconciled PR stands at a comment review |
| `merge-or-wait` | the PR's latest review approves **and no connected issue has an open blocker** — the merge gate (§forgejo/decisions/merge-gate); the merge policy is the prompt's |
| `rebase` | an open PR is not mergeable (conflicts) |
| `decompose` | an open issue carrying the `ideation` label was opened, edited, or had its labels changed; or the reconciliation sweep finds an open issue carrying the `ideation` label, or an open `outcome` tracker that is still blocked and whose `ideation` blocker is closed — a commissioned forest stays under curation until its tracker closes; watermark-exempt on the sweep (§forgejo/reconciliation/watermark-exemptions) |
| `wrap-up` | the reconciliation sweep finds an open issue carrying the `outcome` label with no open blockers — watermark-exempt (§forgejo/reconciliation/watermark-exemptions) |
| `reject-pr` | a PR is connected to work that is not yet commissioned: a **connected issue** — any non-PR issue the PR blocks, every one being tested — is blocked by an **open** `ideation` issue; tested when a PR opens, when a review is submitted, and by the reconciliation sweep, ahead of every review, rebase, fix and merge decision; the prompt closes the PR, names the breach, and reports it |

## Transition table

| When (event + re-fetched state) | Decision |
|---|---|
| issue opened (open, all blockers closed, no connected PR) | `implement` |
| issue opened (open, authored by the configured decomposer identity) | hold off — the reason names the authorship; only the unblock cascade unblocks it |
| issue opened (blocked, or a connected open PR, or not open) | hold off |
| issue opened / edited / labels changed (issue open, carries the `ideation` label) | `decompose` — the label makes the root, whatever the repository, the title, and the author (the root row wins over the authorship row); attaching the label is the *first* trigger. The event path keeps the revision-keyed watermark guard; the sweep is watermark-exempt (§forgejo/reconciliation/watermark-exemptions) |
| issue comment (issue open, carries the `ideation` label) | hold off — a comment is not a revision of intent; the reason says so |
| issue opened / edited / labels changed / comment (issue carries the `outcome` or `human-task` label) | hold off, naming the label — the sweep is where `wrap-up` fires (§forgejo/reconciliation/watermark-exemptions) |
| issue edited / labels changed / feedback comment (issue open, all blockers closed) | `reassess` |
| issue edited / labels changed / feedback comment (issue open, an open blocker) | hold off — the reason names the open blockers |
| issue edited / labels changed / feedback comment (issue not open) | hold off |
| issue closed | unblock cascade |
| PR opened (a connected issue blocked by an open `ideation` issue) | `reject-pr` |
| PR opened (open, mergeable) | `review` |
| PR opened (open, not mergeable) | `rebase` |
| PR synced (open, not mergeable) | `rebase` |
| PR synced (open, mergeable, no review) | `review` |
| PR synced (open, mergeable, has a review) | `re-review` |
| review submitted (a connected issue blocked by an open `ideation` issue) | `reject-pr` |
| review submitted (latest review requests changes) | `fix-feedback` |
| review submitted (latest review approves, no connected issue has an open blocker) | `merge-or-wait` |
| review submitted (latest review approves, a connected issue has an open blocker) | hold off — the reason names the open blockers (§forgejo/decisions/merge-gate) |
| review submitted (latest review is anything else) | hold off |
| PR merged | unblock cascade (for the connected issue) |
| PR closed without merging | hold off |
| PR commented (open) | as reconcile — the sweep's logic: the re-fetched PR state decides the next action |
| reconcile — open issue with the `ideation` label | `decompose`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue, no open blockers | `wrap-up`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue with open blockers, its `ideation` blocker closed | `decompose`, revision notwithstanding — the forest is commissioned and unfinished, so curation continues (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue with open blockers, its `ideation` blocker open | hold off, naming the root — the forest is not yet commissioned, and the root's own row is the pulse: a forest is curated once per sweep |
| reconcile — open `outcome` issue with open blockers and no `ideation` blocker | hold off, naming the absence — a tracker not gated by a root is not a forest's tracker |
| reconcile — the sweep's synthetic current-state event | as issue opened (issue); for a PR, `reject-pr` when a connected issue is blocked by an open `ideation` issue and otherwise as PR synced — except when the last activity is a review: changes-requested or comment dispatches `fix-feedback`, approved dispatches `merge-or-wait` — held off, naming the open blockers, when a connected issue has one (§forgejo/decisions/merge-gate) |

The **reject-pr test precedes the review and rebase rows** on every path
that can dispatch review, re-review, fix-feedback, or merge-or-wait: it
applies when a PR opens, ahead of the review-following decisions of a
submitted review — so a gate that closed after the PR opened cannot be
walked through by the next approving review — and on the whole PR path of
the reconciliation sweep, whose re-fetch performs the same one hop. The
remaining boundary is the **PR synced event**: a push to an open PR
decides review or re-review without the test — its re-fetch grows no hop,
keeping the push path cheap — and the next review submission or sweep
applies the test. Stated here so the boundary is a known limitation and
not a silent contradiction of the gate.

A PR's **connected issues** are the non-PR issues the PR blocks
(§forgejo/webhook/re-fetch) — **recorded** blocker relationships, never
inferred ones. Forgejo creates no PR→issue dependency from a `Fixes #NN`
keyword in a PR body, and the engine's client is read-only
(§forgejo/client/operations), so the dependency must be recorded when
the PR is opened: the workflow the `implement` prompt dispatches — the
`/issue-implementer` invocation (§forgejo/config/canned-prompts) —
records `issue-block <PR> → <issue>` for each issue the PR closes,
cross-repo targets included, and the engine only reads it. That
instruction lives in the canned prompts, deployed outside this
repository (§forgejo/config/loading); **it does not exist yet**, and
until it does the `reject-pr` row cannot be reached — a PR whose
opening recorded no dependency has no connected issues and decides as
before.

When **any** connected issue is blocked by an **open** issue carrying
the reserved `ideation` label — work whose gate has not opened: an
ideation issue's closure *is* the commissioning of its forest
(§forgejo/decisions/commissioning) — the PR is work started before
its gate, and `reject-pr` is decided. The test applies to **every**
connected issue, not just the first: any blocked connected issue is work
started before its gate, so the decision does not depend on the order
the API returns the relationships in. Reserved names are matched exactly
and case-sensitively, as elsewhere in the engine: renaming the label in
the UI disables the test silently, a fragility accepted with the name.
A connected issue's **own** state is not consulted — the gate is the
blocker's — so a PR opened against a closed connected issue still
blocked by an open ideation issue is rejected. A blocked *closed*
ideation issue, an open blocker without the label, and the absence of
any connected issue all decide as before.

The "latest review" is the PR's most recently submitted review. An
issue that *became unblocked* is dispatched `implement` through the
unblock cascade, under the same conditions as an opened issue.

**Reserved labels.** An issue carrying the `ideation`, `outcome`, or
`human-task` label is **never** an `implement` or `reassess` candidate:
whatever the repository's roles — a mislabelled outcomes ticket, or one
in a repository holding both roles, must not wake the implementer. The
`ideation` label makes its issue a **root**: an open issue carrying it
is decided `decompose` on the issue-opened, issue-edited, and
labels-changed rows — the label, not the repository and not the title,
is what makes a root. The body of an edit is the revision of intent the
decomposer re-cures against, so an edit re-fires `decompose`; a comment
is not a revision of intent, so a comment on an open root holds off.
A root that is closed on re-fetch holds off on every row: closing it is
the release gate, and the unblock cascade dispatches the forest members.
The `outcome` and `human-task` labels keep their hold-offs, naming the
label: the `human-task` label marks a ticket claimed by a human and is
never an `implement` candidate anywhere in the engine, the unblock
cascade included (§forgejo/decisions/unblock-cascade). The role model
(§forgejo/webhook/repository-roles) is the load-bearing boundary; this
rule is insurance. When an issue carries `ideation` alongside another
reserved label, `decompose` wins: a root is decomposed before anything
else is decided for it — a `human-task` claim on a root withholds
`implement`, not `decompose`, since decomposition is precisely the work
a human may have claimed on a root. The root row likewise wins over the
authorship hold-off: a root authored by the configured decomposer
identity is still decomposed.

Activity on a **blocked** issue — an edit, a label change, a comment on
an issue with an open blocker — has nothing to reassess: the work is
held by the blocker, not by the activity, so the decision holds off and
names the open blockers (each as `repo#number`, cross-repo included).
This is what makes it safe to attach go-ahead labels to forest members
in any order: labelling a blocked issue wakes no implementer. When the
issue becomes unblocked, the cascade — not the activity — dispatches
`implement`.

**Authorship of a forest member.** An issue whose author is the configured
decomposer identity (§forgejo/config/decomposer-identity) is **never** an
`implement` candidate on the event path or on the reconciliation sweep (which
decides as issue opened): the decision holds off and the reason names the
authorship. The rule exists because `issues:opened` arrives the instant the
decomposer creates a member, while the edge blocking it on its ideation ticket is
attached afterwards and emits no delivery — so the re-fetch sees zero blockers and
`implement` would be vacuously correct for work that has not been commissioned.
Authorship is the only fact about a member that is true at the instant of its
creation, and the identity is the operator's declaration rather than an inference;
the engine cannot forge it, because the identities it dispatches cannot edit an
issue's author. The comparison is **exact** — no case folding, no substring
matching — the same shape as the reviewing-author sender guard
(§forgejo/decisions/loop-prevention). With no identity configured the rule is
inert (§forgejo/config/decomposer-identity). The rule denies `implement`
candidacy only: it does not deny `reassess`, which the open-blocker rule above
governs. The **unblock cascade is not narrowed**: a decomposer-authored member
still dispatches `implement` when the cascade finds it newly unblocked —
the ideation ticket's closure is that member's last issue-side blocker, and
the cascade is what dispatches it.

A PR's **last activity is a review** when its latest review is a
changes-requested, approved, or comment review submitted against the PR's
current head (the review's commit ID equals the head SHA) — no commit has
been pushed since the review. A reconciled PR standing at such a review
decides as if that review had just been submitted — except that a comment
review, which holds off on the event path, dispatches `fix-feedback`: the
review is feedback for the implementer, and the sweep moves the PR along by
triggering the implementer, not the reviewer. Changes-requested dispatches
`fix-feedback`, approved dispatches `merge-or-wait` — unless the merge
gate holds it off on an open blocker (§forgejo/decisions/merge-gate). A
PR whose latest
review is anything else (e.g. dismissed), or whose latest review predates
the current head, is reconciled as PR synced.

## Merge gate

`merge-or-wait` is dispatched **only when no connected issue of the PR
has an open blocker**. A PR's **connected issues** are the non-PR issues
the PR blocks (its dependency rows, read cross-repo where the connected
issue lives elsewhere); an issue's **blockers** are the issues that
block it. A go-ahead label on the issue or the PR authorises merging on
its own — it does not authorise merging **past a blocker**. The gate is
the merge-path twin of the open-blocker rule that holds off `reassess`
(§forgejo/decisions/transition-table), and every path that dispatches
`merge-or-wait` consults it through one shared test: the review event,
the sweep's PR path, and the held PR the unblock cascade unblocks.

The PR's **own entry** in a connected issue's blocker list is not a
blocker: the PR *is* the connected issue's delivery vehicle (the
issue-blocks record that connects them makes the PR one of the issue's
blockers), so the test excludes the PR itself and counts every other
open blocker.

A blocked merge **holds off**; it does not reject. `reject-pr` means
"this PR should not have been opened" — the work-not-yet-commissioned test of
§forgejo/decisions/transition-table — and is decided ahead of this gate;
a PR that opened clean and was blocked afterwards is legitimate work
waiting to land. It holds off silently on the merge
path — the hold-off reason names each open blocker as `repo#number`,
cross-repo included — and the unblock cascade (or the sweep) moves it
when its blockers close.

The gate reads dependency rows of the connected issues; where none are
recorded, the gate sees no blockers and merges as before. The
recording half lives outside the decision function (the workflow the
`implement` prompt dispatches records `issue-block PR → issue`,
cross-repo targets included); until that record exists the gate
cannot fire — it never blocks more than the graph states.

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
already-dispatched `(action, revision)` is not re-dispatched. A
cascaded key carrying a reserved label holds off, naming the label:
the cascade dispatches `implement`, and a labelled issue is decided by
the sweep (§forgejo/reconciliation/watermark-exemptions).

The cascade dispatches `implement`: an issue newly unblocked but
carrying a reserved label (`ideation`, `outcome`, `human-task` — see
§forgejo/decisions/transition-table) is held off, naming the label,
instead of dispatched. Each cascaded key's admissibility is judged by
the roles of the repository **holding the key**, never the repository
whose closure rooted the cascade (§forgejo/webhook/repository-roles).

An issue whose remaining open blockers are **its connected open PRs**
is not an implement candidate — the work exists as a PR, and the
`implement` condition denies a connected open PR. When the closure that
rooted the cascade closes such an issue's last *issue-side* blocker, the
cascade instead moves those PRs: each connected open PR becomes a
cascade key of the pull-request kind, dispatched `merge-or-wait` — the
merge the gate held off (§forgejo/decisions/merge-gate) fires on the closure
that removed the last blocker, exactly as a newly unblocked issue is
dispatched with `implement`. A cascaded PR key pays the same admission,
insurance, and watermark as an issue key: the
re-fetch of the PR decides it, and only a decision of `merge-or-wait`
dispatches — a PR not standing at an approved head stays silent and the
sweep remains the backstop. The cascade dispatches nothing else for
PRs.

The walk itself is label-blind: labelled nodes are visited and
traversed, so graph walking stays independent of what is finally
dispatched.

## Loop prevention

Four mechanisms, in this order:

1. **The machine is terminating.** Each action's side-effect maps to
   the *next* transition, never a re-run of the same one: a review
   does not push commits; a commit push triggers a review; a
   changes-requested review triggers a fix; a fix pushes commits.
2. **Idempotency + watermark.** The same `(event, state, revision)`
   does not re-dispatch the same action: when the watermark already
   records the action at the current revision (the PR head sha, or the
   issue's `updated_at`), the decision holds off. The guard is
   revision-keyed on every path. The watermark-exempt actions
   `decompose` and `wrap-up` are exempt only **on the sweep**, whose
   rows for them decide without consulting the watermark: state is
   their watermark (§forgejo/reconciliation/watermark-exemptions), and
   the agent's silence-on-noop keeps the re-firing quiet.
3. **Targeted per-transition sender guards**, only where an agent's
   *own* action would re-trigger the *same* action: a commit pushed by
   the PR's reviewing author (the author of its latest review) must
   not re-review that same commit. Cross-persona triggering (a
   reviewer's changes-requested leading to the implementer's fix) is
   intended and unguarded.
4. **Targeted authorship guards**, where an agent's *own* creation would
   otherwise re-trigger the *same* action: an issue authored by the configured
   decomposer identity is never an `implement` candidate on the event path or the
   sweep, because the creation delivery is new to the key and no watermark can
   show that the work is not yet commissioned. The guard is by the issue's
   **author**, not the delivery's **sender**, and the cascade that unblocks the
   member is unguarded (§forgejo/decisions/transition-table).

## Inputs

**Event** — type, the affected object (repo, kind, number), and the
sender's login. The event types are: issue opened / edited / labels
changed / commented / closed; PR opened / synced / reviewed / merged /
closed / commented; and the synthetic `reconcile` event of the
reconciliation sweep. A general comment on a pull request arrives as
`pull_request_comment` — the PR-side analogue of an issue comment — and
decides as reconcile (§forgejo/decisions/transition-table).

**State** — the re-fetched state of the affected object: the issue or
PR itself (an issue carrying its **author**, the login of whoever opened
it, §forgejo/client/operations); the issue's blockers (with their states)
and its connected open PRs, for the `implement` and `reassess`
conditions; the PR's reviews, for the review transitions; for PR opened,
PR reviewed and the sweep's PR path, the issues the PR blocks and each
issue's blockers, for the `reject-pr` test (§forgejo/decisions/actions)
and for the merge gate (§forgejo/decisions/merge-gate), which reads the
same rows on the PR reviewed event and the sweep's synthetic PR event;
and, for close events, the blocker-graph root — the closed issue — with
each node carrying the issues it blocks and the issues that block it.

Carried on the state is the one **declared configuration input** the
function consults: the configured **decomposer identity**
(§forgejo/config/decomposer-identity), which the engine passes verbatim
rather than inferring. It is empty when the key is unset, in which case
the authorship rule is inert.

**Watermark** — the last dispatch for the object's key
(§forgejo/state/watermark), or none.
