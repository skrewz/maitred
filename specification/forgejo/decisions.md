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
unblocked, each warranting a dispatch of `implement` — or of `wrap-up`
when the newly unblocked issue carries the `outcome` label
(§forgejo/decisions/unblock-cascade) — plus the cascade hold-offs: an
open tracker the walk finds still blocked, its reason naming the
blockers that remain open.

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

## Forest teardown

`teardown-forest` is the human's order to **un-make a forest**. It is a
modifier of the root row and nothing else: an open `ideation` issue that
also carries it dispatches `teardown-forest` where it would otherwise
dispatch `decompose`, and the decomposer deletes the member issues and
sets that root created, retracting the edges as it goes. The root itself
is never deleted — it is the human's issue, and the human closes it once
the teardown is reported.

**Pre-closure only.** An `ideation` issue's closure *commissions* the
forest (§forgejo/decisions/commissioning), and commissioning is one way.
A closed root holds off on every row, teardown label included: the
forest it made is history, its members may have landed, and no label
retroactively un-happens a commission.

**Idempotent by construction.** The trigger is a state relation — the
label standing on an open root — so the sweep re-fires it until the
forest is gone (§forgejo/reconciliation/watermark-exemptions), and a run
that finds nothing left deletes nothing and says nothing. Re-running the
teardown is therefore the recovery from a teardown that died half way
through, which is why the engine re-fires rather than trusting one
delivery.

**What the label cannot reach.** On an issue carrying no `ideation`
label the label decides nothing: it is no kind of work item and it
retires no ticket. The decision holds off naming that the label modifies
the root row, so a misapplied teardown reads in the decision log as a
misapplied teardown rather than waking the implementer on the very
ticket the human meant to retire.

**One root names the forest, and only through the edges.** The dispatch
carries a single issue, so the forest is the transitive closure of the
recorded blocker edges out of that root and the issues blocked by its
tracker. The repository names nothing — other roots share it; the labels
name nothing — they are shared vocabulary, not a membership test; and the
root's prose names nothing, however many numbers it cites. An issue that
another root also blocks belongs to that forest: this order retracts the
edge and leaves the issue, because deletion is the one act no cascade
rehearses and no watermark can undo. The traversal rules live in the
canned prompt (§forgejo/config/canned-prompts); the boundary belongs here
so the contract the engine offers — one root, its edges, nothing inferred
— is specified rather than implied.

**The engine counts before it asks.** Whether a forest stands is not a
matter of opinion: the root blocks issues, or it blocks none. A root
carrying the order therefore has its **downstream** edges read on the
re-fetch — one `IssueBlocks` hop, and only when both labels are present,
so an ordinary root pays nothing — and a root blocking nothing dispatches
no agent, naming the empty forest as the state fact. This is what ends
the re-firing: the exemption asks again while the forest stands, and a
forest an earlier run deleted, or one never made, stops the asking by
itself. Without the count the label would summon a silent agent every
sweep forever, which is the waste the test exists to avoid — and a root
labelled before it was ever decomposed, the common case, would cost a run
per sweep to report that there is nothing to report. The empty forest is
not a defect and not a hold-off worth an agent; the closed member is
neither, since a member that landed still owes the report the run makes
for it.

**The tracker needs no rule of its own.** Deleting members empties the
tracker's blocker list, and an emptied tracker looks finished to the
`wrap-up` row — so the protection matters, but it comes from the graph:
the order is pre-closure, which means the root is *open* while a teardown
runs, and an open root is an open blocker of its tracker. Both the
tracker's rows already hold off on an open root
(§forgejo/reconciliation/watermark-exemptions), and deleting every member
cannot empty the list while that edge stands. A label that reaches a
**closed** root therefore changes nothing anywhere — which is the point:
a stale label must not freeze its tracker's `wrap-up` or its curation
rows forever, naming a terminal state as though it were transient. If a
commissioned forest must be retired, the human reopens the root or the
label is removed; the label has no power past the gate.

**Deletion is a right the engine does not hold, and cannot check.** Deleting an
issue is not the same right as creating one: a forge grants it to owners, and to
collaborators holding admin on that repository, and to nobody else. Here that grant
is held **per repository, by allowlist** — an account with admin across the
organisation is a wider risk than a forest left standing, and no unit-scoped
permission expresses "admin, but only for issues". So the permission is a fact about
the *dispatched identity*, not about the decision, and the engine neither holds nor
can probe it: it has no write access of any kind. The canned prompt therefore proves
the right **before it creates** (§forgejo/config/canned-prompts) — one read of every
repository a create would land in, and the whole commission refused if any one of
them withholds it — so the refusal surfaces while the forest is still an idea rather
than after six members exist in a repository the persona cannot empty. A teardown
that is refused anyway reports the repositories that refused it, changes nothing, and
is asked again next sweep, because a standing forest is an instruction and the engine
has no way to tell a refusal that will pass tomorrow from one that never will.

## Actions

The action names form the enum the engine config maps to canned
prompts:

| Action | Dispatched when |
|---|---|
| `implement` | the issue is open, unblocked (all its blockers closed), has no connected open PR, carries no reserved label, and is not authored by the configured decomposer identity — the issue was opened or became unblocked, and is not an `outcome` tracker (one that becomes unblocked is a `wrap-up` candidate) |
| `reassess` | an open issue with no open blockers was edited, had its labels changed, or received a feedback comment (the prompt decides whether to implement), and it carries no reserved label |
| `review` | a mergeable PR opened, or synced while it has no review, or an open mergeable PR carrying the `trigger-review` label while it has no review (§forgejo/decisions/label-triggered-review) |
| `re-review` | a mergeable PR synced while it has a review, or an open mergeable PR carrying the `trigger-review` label while it has a review (§forgejo/decisions/label-triggered-review) |
| `fix-feedback` | the PR's latest review requests changes, or a reconciled PR stands at a comment review |
| `merge-or-wait` | the PR's latest review approves **and no connected issue has an open blocker** — the merge gate (§forgejo/decisions/merge-gate); the merge policy is the prompt's |
| `rebase` | an open PR is not mergeable (conflicts) |
| `decompose` | an open issue carrying the `ideation` label was opened, edited, or had its labels changed; or the reconciliation sweep finds an open issue carrying the `ideation` label, or an open `outcome` tracker that is still blocked and whose `ideation` blocker is closed — a commissioned forest stays under curation until its tracker closes; watermark-exempt on the sweep (§forgejo/reconciliation/watermark-exemptions) |
| `wrap-up` | the reconciliation sweep finds an open issue carrying the `outcome` label with no open blockers — watermark-exempt (§forgejo/reconciliation/watermark-exemptions); or the unblock cascade newly unblocks an open issue carrying the `outcome` label (§forgejo/decisions/unblock-cascade) |
| `teardown-forest` | an open issue carrying the `ideation` label **also** carries the `teardown-forest` label, **and blocks at least one issue**: the human has ordered the forest that root commissioned to be deleted, and there is a forest to delete (§forgejo/decisions/forest-teardown); watermark-exempt on the sweep while the forest stands (§forgejo/reconciliation/watermark-exemptions) |
| `reject-pr` | a PR is connected to work that is not yet commissioned: a **connected issue** — any non-PR issue the PR blocks, every one being tested — is blocked by an **open** `ideation` issue; tested when a PR opens, when a review is submitted, and by the reconciliation sweep, ahead of every review, rebase, fix and merge decision; the prompt closes the PR, names the breach, and reports it |

## Transition table

| When (event + re-fetched state) | Decision |
|---|---|
| issue opened (open, all blockers closed, no connected PR) | `implement` |
| issue opened (open, authored by the configured decomposer identity) | hold off — the reason names the authorship; only the unblock cascade unblocks it |
| issue opened (blocked, or a connected open PR, or not open) | hold off |
| issue opened / edited / labels changed (issue open, carries both the `ideation` and the `teardown-forest` label, blocks at least one issue) | `teardown-forest` — the teardown row wins over the root's `decompose` row, so a forest under teardown is never re-commissioned while it is being deleted (§forgejo/decisions/forest-teardown) |
| issue opened / edited / labels changed (issue open, carries both labels, blocks no issue) | hold off, naming the empty forest — the engine can see there is nothing to delete, so no agent is asked to discover it (§forgejo/decisions/forest-teardown) |
| issue opened / edited / labels changed / comment (issue open, carries the `teardown-forest` label without `ideation`) | hold off, naming that the label modifies the root row and retires no work item — a kind label carried alongside is named first, since what an issue *is* outweighs what a label *modifies* (§forgejo/decisions/forest-teardown) |
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
| PR carrying the `trigger-review` label (open, mergeable, no review) | `review` — explicit request (§forgejo/decisions/label-triggered-review) |
| PR carrying the `trigger-review` label (open, mergeable, has a review) | `re-review` — explicit request (§forgejo/decisions/label-triggered-review) |
| review submitted (a connected issue blocked by an open `ideation` issue) | `reject-pr` |
| review submitted (latest review requests changes) | `fix-feedback` |
| review submitted (latest review approves, no connected issue has an open blocker) | `merge-or-wait` |
| review submitted (latest review approves, a connected issue has an open blocker) | hold off — the reason names the open blockers (§forgejo/decisions/merge-gate) |
| review submitted (latest review is anything else) | hold off |
| PR merged | unblock cascade (for the connected issue) |
| PR closed without merging | hold off |
| PR commented (open) | as reconcile — the sweep's logic: the re-fetched PR state decides the next action |
| reconcile — open issue carrying both the `ideation` and the `teardown-forest` label, blocking at least one issue | `teardown-forest`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open issue carrying both labels, blocking nothing | hold off, naming the empty forest: the re-firing ends where the forest does (§forgejo/decisions/forest-teardown) |
| reconcile — open issue with the `ideation` label | `decompose`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue, no open blockers | `wrap-up`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue with open blockers, its `ideation` blocker closed | `decompose`, revision notwithstanding — the forest is commissioned and unfinished, so curation continues (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue whose `ideation` blocker is **open** and carries the `teardown-forest` label | hold off, as any tracker with an open root does: the teardown order needs no row of its own, and the root's own edge is what keeps an emptied tracker from reading as finished (§forgejo/decisions/forest-teardown) |
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
unblock cascade, under the same conditions as an opened issue — except
that a newly unblocked issue carrying the `outcome` label is a
`wrap-up` candidate instead: its work is the sum of its members, and
wrapping up, not implementing, is what finishes it.

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
A root that is closed on re-fetch holds off on every row: closing it
commissions the forest; the unblock cascade dispatches its members.
The `outcome` and `human-task` labels keep their hold-offs, naming the
label: the `human-task` label marks a ticket claimed by a human and is
never an `implement` candidate anywhere in the engine, the unblock
cascade included (§forgejo/decisions/unblock-cascade). The role model
(§forgejo/webhook/repository-roles) is the load-bearing boundary; this
rule is insurance. The `teardown-forest` label is a **modifier of the root
row**, not a kind label: it makes no issue a root, a tracker or a claim,
and on an issue without `ideation` it holds off naming that it modifies
the root row. The kind labels are tested first, so an issue carrying
`human-task` or `outcome` alongside it is described by what it *is*
(§forgejo/decisions/forest-teardown).
When an issue carries `ideation` alongside another
reserved label, `decompose` wins — unless the issue also carries
`teardown-forest`, in which case teardown wins: a root is decomposed before anything
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

## Label-triggered review

The `trigger-review` label is the **explicit request for an agentic review**.
When an open, mergeable pull request carries it, the decision dispatches a
review for that revision regardless of the review state the transition table
would otherwise produce: `review` when the PR has no review, `re-review` when
it already does. The label is an *override of the trigger*, not of the review's
content — the reviewing agent decides the outcome (approve, request changes,
comment) and the merge policy applies unchanged (§forgejo/decisions/actions).

The label restores the pre-engine affordance where a human, by labelling, asked
for a review that the state transitions alone would not have raised. The
dispatch is keyed to the label's presence at the current revision so it does
not re-fire the review while the label is merely present
(§forgejo/decisions/loop-prevention); removing and re-applying the label at a
new revision is how an explicit re-ask is made. The reviewing-author sender
guard does not suppress a label-requested review — the label is an explicit
request, not the author's own push.

`trigger-review` is **additive**: it never suppresses the state-driven
`review` / `re-review` transitions, and it is distinct from the reserved labels
(`ideation`, `outcome`, `human-task`), which gate work items rather than
request reviews (§forgejo/decisions/transition-table).

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
repository), and adds one dispatch per issue that is now unblocked:
open, with **all** its blockers closed — `implement`, or `wrap-up`
when the issue carries the `outcome` label: a tracker whose last
blocker just closed is a tracker whose work is finished. An issue
carrying both `ideation` and `outcome` is not a `wrap-up` candidate
(`decompose` wins over `wrap-up`); it takes the `implement` dispatch
and the reserved-label hold-off naming `ideation`.

The cascade is **transitive**: a blocked issue that is itself closed
cascades onward to the issues it blocks (A→B→C). It **terminates**
without double-dispatching: each issue is visited once. The engine
applies each cascaded key's own watermark before dispatching, so an
already-dispatched `(action, revision)` is not re-dispatched. A
cascaded key carrying `ideation` or `human-task` holds off, naming the
label: it is decided by the sweep
(§forgejo/reconciliation/watermark-exemptions); a key carrying
`outcome` dispatches `wrap-up`.

A newly unblocked `outcome` tracker dispatches `wrap-up`, never
`implement`: a tracker is never an implement candidate anywhere in
the engine, the cascade included (§forgejo/decisions/transition-table).
Each cascaded key's admissibility is judged by the roles of the
repository **holding the key**, never the repository whose closure
rooted the cascade (§forgejo/webhook/repository-roles) — which is how
a cascade rooted at an issue in a work-item repository can emit
`wrap-up` into an outcomes repository: the cascaded action is judged
against the roles of the repository holding the tracker, whatever
rooted the walk. The dispatch re-checks the action against the
re-fetched issue — the state wins over the graph data: an `implement`
key carrying any reserved label holds off, naming the label; a
`wrap-up` key whose issue no longer carries `outcome`, or now carries
`ideation`, holds off saying so. Either way the sweep decides the
issue afresh.

A still-blocked open tracker the walk visits is **not silent**: the
cascade records a hold-off for its key naming which blockers remain
open (each as `repo#number`), so closing one of several blockers logs
why `wrap-up` did not fire. A blocker **holds** the tracker unless its
state is `closed`: a state the engine does not recognise holds as
surely as `open` does, and is named rather than passed over in silence.
This holds for every way the walk can visit a tracker without emitting
for it — including a tracker whose remaining open blockers are only its
connected open PRs: those PRs are emitted `merge-or-wait` as for any
other issue, and the tracker itself is named as still held, by a
blocker whose own merge is what will dispatch its `wrap-up`. The
hold-off carries the tracker as the graph showed it, so the decision
log entry records the state and revision it was made at instead of
blanking them (§forgejo/observability/the-decision-log,
§forgejo/observability/the-dashboard-view) — an unrelated close event
must not downgrade a tracker's dashboard row. Where the tracker's
`ideation` root is **closed** the forest is commissioned and unfinished,
so the hold-off says the sweep curates it
(§forgejo/reconciliation/watermark-exemptions) rather than promising a
`wrap-up` the state does not call for. An unlabelled issue found still
blocked stays silent — the walk merely passes through.

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
dispatched; labels are consulted only at emission.

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
   not re-review that same commit. The guard yields to an explicit
   request: a PR carrying the `trigger-review` label is reviewed
   whatever its author pushed (§forgejo/decisions/label-triggered-review).
   Cross-persona triggering (a reviewer's changes-requested leading to
   the implementer's fix) is intended and unguarded.
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
conditions; the PR's reviews, for the review transitions; the PR's labels,
for the label-triggered review override
(§forgejo/decisions/label-triggered-review); for PR opened,
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
