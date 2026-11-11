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
sets that root created, retracting the edges as it goes. **No tracker is
deleted**: the order tears down every other member and leaves any tracker it
reaches standing, its own forest's included. The root itself is never deleted
— it is the human's issue, and the human closes it once the teardown is
reported.

**A switch, not a message.** The label states what to do *now*; it
carries no history and no verdict on the idea, and both of its
transitions are ordinary events on the same root. **Adding** it orders
the teardown; **removing** it leaves an open `ideation` root, which is
the root row, which dispatches `decompose`
(§forgejo/decisions/transition-table). A human flips it often and
cheaply — an empty teardown is a run that deletes nothing, a rebuilt
forest is one curation run, and either can be reversed with the same two
clicks — so the engine draws no inference from the direction a label
moved, and offers no hold-off waiting for prose to explain what the
label already states. The two rows never fight: teardown wins while the
label stands and the root row resumes the moment it does not.

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

**One root names the forest, and only through its membership edges.** The
dispatch carries a single issue, so the forest is **exactly** the issues the
recorded `blocks` edges out of that root reach — a membership edge is what
makes a member, and the enumeration is one hop, not a closure. A work-order
edge between two members orders work and admits no member; an edge out of a
tracker gates work belonging to whichever forest asked for that dependency,
and admits no member either. The repository names nothing — other roots share
it; the labels name nothing — they are shared vocabulary, not a membership
test; and the root's prose names nothing, however many numbers it cites. An
issue that another root also blocks belongs to that forest: this order
retracts the edge and leaves the issue, because deletion is the one act no
cascade rehearses and no watermark can undo. The traversal rules live in the
canned prompt (§forgejo/config/canned-prompts); the boundary belongs here so
the contract the engine offers — one root, its membership edges, nothing
inferred — is specified rather than implied.

**The engine counts what the order can delete.** Whether a forest still stands
is not a matter of opinion: the root blocks members the order would delete, or
it blocks none. A root carrying the order therefore has its **downstream**
edges read on the re-fetch — one `IssueBlocks` hop, and only when both labels
are present, so an ordinary root pays nothing — and the count is of the issues
that hop names **other than an `outcome` tracker**: a tracker survives the
order, so its membership edge is a standing edge that says nothing about
whether a forest remains. A root whose only downstream edge reaches its tracker
dispatches no agent, naming what it still blocks. This is what ends the
re-firing: the exemption asks again while a deletable member stands, and a
forest an earlier run tore down, or one never made, stops the asking by itself.
The spared tracker is why the count excludes trackers at all: it keeps the root
blocking one issue for the life of that root, so a count that included it would
re-fire the order on every sweep for ever — the waste this paragraph exists to
prevent. A root labelled before it was ever decomposed, the common case, would
likewise cost a run per sweep to report that there is nothing to report. The
empty forest is not a defect and not a hold-off worth an agent; the closed
member is neither, since a member that landed still owes the report the run
makes for it.

**A tracker is left in peace.** The order deletes the members that deliver
something and spares every tracker it reaches, because a tracker's outbound
edges are not this forest's. Once an outcome is worth waiting on, other graphs
point at it — an initiative, a small fix, or a member of another root blocked
by the tracker before that forest was commissioned — and deleting the issue
beneath those edges retracts them without asking, opening a gate another forest
is standing on. A tracker is therefore never deleted, by this order or by any
walk that reaches it: the record, and whatever waits behind it, stay where the
human can read them. The spared tracker stays **gated**, not finished: its
membership edge from the root survives, the order is pre-closure so that root
is open, and an open root is an open blocker — so `wrap-up` does not fire on the
emptiness the teardown just caused, and nothing downstream of the tracker moves
(§forgejo/reconciliation/watermark-exemptions). The human's next act decides:
closing the root lets the cascade unblock the tracker and dispatch its
`wrap-up`; removing the label lets the next curation run rebuild the forest
onto the tracker already standing, rather than making a second one. A label
that reaches a **closed** root therefore changes nothing anywhere — a stale
label must not freeze its tracker's `wrap-up` or its curation rows forever,
naming a terminal state as though it were transient. If a commissioned forest
must be retired, the human reopens the root or the label is removed; the label
has no power past the gate.

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
| `implement` | the issue is open, unblocked (all its blockers closed), has no connected open PR, carries no reserved label and does not carry `forest-seeding`, and is not an `outcome` tracker (one that becomes unblocked is a `wrap-up` candidate) — the issue was opened, became unblocked, or was reconciled into that state (§forgejo/decisions/loop-prevention, §forgejo/decisions/commissioning) |
| `reassess` | an open issue with no open blockers was edited, had its labels changed, or received a feedback comment (the prompt decides whether to implement), and it carries no reserved label |
| `review` | the **fan-out over the configured lenses**: a mergeable PR opened, or synced, dispatches each lens with no undismissed approval at the head (§forgejo/decisions/review-lenses); so does an open mergeable PR carrying the `trigger-review` label, at the current revision (§forgejo/decisions/label-triggered-review); watermark-exempt per lens on the sweep while that lens has reported nothing at the head (§forgejo/reconciliation/watermark-exemptions) |
| `fix-feedback` | every configured review lens has reported at the PR's current head and the reports are not all approvals (§forgejo/decisions/review-lenses) — **and** the PR's author is the configured implementer identity; a PR authored by anyone else holds off, its feedback theirs to fix (§forgejo/config/implementer-identity, §forgejo/decisions/loop-prevention, s-git/maitred#143); while any lens has not reported at head the action holds off naming the outstanding lenses, whose dispatches the fan-out row advances; watermark-exempt on the sweep while the chain reports feedback at the current head (§forgejo/reconciliation/watermark-exemptions) |
| `merge-or-wait` | **every configured lens holds an undismissed approval at the PR's current head**, no reviewer's latest undismissed review at head is a non-approval (§forgejo/decisions/review-lenses), **the work it delivers carries the `agentic-auto-merge` opt-in — on the PR or on the issue its body closes** — the label is a precondition of the dispatch on every path, so an approved PR with no merge opt-in on either seat holds off, its reason naming the missing label and the connected issues (§forgejo/decisions/merge-gate, s-git/maitred#179) — **and no connected issue has an open blocker** — the merge gate (§forgejo/decisions/merge-gate); the merge policy is the prompt's; never for a PR whose connected issue is a member of an open sync set — landing is the set's decision (§forgejo/decisions/sync-set); a labelled member of no open set — its set retired — reads as stale authoring, not a gate, and the merge gate decides on (§forgejo/decisions/sync-set); watermark-exempt on the sweep when the PR or the issue its body closes carries `agentic-auto-merge` (§forgejo/decisions/merge-gate, §forgejo/reconciliation/watermark-exemptions) |
| `rebase` | an open PR is not mergeable (conflicts) |
| `decompose` | an open issue carrying the `ideation` label was opened, edited, had its labels changed, or received a comment authored by anyone other than the configured decomposer identity (§forgejo/decisions/loop-prevention, s-git/maitred#147); or the reconciliation sweep finds an open issue carrying the `ideation` label, or an open `outcome` tracker that is still blocked and whose `ideation` blocker is closed — a commissioned forest stays under curation until its tracker closes; watermark-exempt on the sweep (§forgejo/reconciliation/watermark-exemptions) |
| `wrap-up` | the reconciliation sweep finds an open issue carrying the `outcome` label with no open blockers — watermark-exempt (§forgejo/reconciliation/watermark-exemptions); or the unblock cascade newly unblocks an open issue carrying the `outcome` label (§forgejo/decisions/unblock-cascade) |
| `teardown-forest` | an open issue carrying the `ideation` label **also** carries the `teardown-forest` label, **and blocks at least one issue that is not an `outcome` tracker**: the human has ordered the forest that root commissioned to be deleted, and a member the order would delete remains to delete (§forgejo/decisions/forest-teardown); watermark-exempt on the sweep while such a member stands (§forgejo/reconciliation/watermark-exemptions) |
| `sync` | an open issue carrying the `sync` label — a **landing set**: changes that must land together — has every blocking issue either closed or standing with its delivering PR approved at head and mergeable while carrying `agentic-sync-merge`; "approved at head" is the lens gate — every configured lens holds an undismissed approval at that PR's head (§forgejo/decisions/review-lenses) (a blocking issue that is itself a pull request is held to that test through its own state); dispatched from the reconciliation sweep, from the unblock cascade's sync-set candidate, and from the approval path's emission re-checked on re-fetch; the watermark is a revision derived from the whole set (§forgejo/decisions/sync-set) |

Reviews are the fan-out over the configured lenses, not a configured
action entry: `review` names the fan-out's dispatch in the decision
log, one emission per lens, and `re-review` is retired with the
single-reviewer model (§forgejo/config/review-lenses).

## Review lenses

A pull request is reviewed by the **ordered lens list** the config
declares (§forgejo/config/review-lenses). The whole model reduces to
one predicate:

> **The lenses to dispatch are the configured lenses with no
> undismissed approving review at the PR's current head.**

A lens's standing is read from the reviews the forge reports: the
lens — compared by its configured `identity`, exact and case-sensitive,
never inferred — has an **undismissed** review at head whose latest
such review approves. Stale-approval dismissal is what makes a push
invalidate the chain: after a push no lens holds an undismissed
approval at the new head, so the fan-out re-fires **all** of them —
parallel at first, only the invalidated ones after a push, the whole
chain again whenever a fix round ends with rejections (the fix pushes
new commits, and the push is the re-ask). The fan-out dispatches one
task per outstanding lens, in config order; the task's dedup key
carries the lens (§forgejo/webhook/the-pipeline), so three review
tasks over one head are three tasks, and a lens mid-flight when the
head moves gets at most a queued duplicate it walks away from on
seeing its own approval at head — a task wasted, not correctness
lost.

Each outstanding lens passes under its own half of the watermark —
the subject slot for its identity at this revision
(§forgejo/state/watermark) — so two lenses dispatched at the same
revision do not hold each other off, and the guard speaks only for
the lens it names. The sweep's fan-out row is exempt per lens while
that lens has reported **nothing** at the head: a dispatch that went
nowhere recovers, a lens that has already spoken at head does not
re-fire (§forgejo/reconciliation/watermark-exemptions). While the slot
names a dispatch at the current head yet the PR carries no review by
that login anywhere, the reason says so — the state cannot tell a
dispatch still mid-flight from a mistyped `identity` that can never
report, but the operator can (§forgejo/config/review-lenses).

The review-following gates read the chain, not "the latest review":

- **fix-feedback** fires only when every lens has reported at head
  and the reports are not all approvals — a reviewer's latest
  undismissed review at head asking for changes (or a comment report,
  on the sweep's row) is feedback for the implementer. While any lens
  has not reported at head the action holds off, the reason naming
  the outstanding lenses, because a fix pushes commits and would
  silence the lenses still reading the old head.
- **merge-or-wait** fires when every lens holds an undismissed
  approval at head, no reviewer's latest undismissed review at
  head is a non-approval, **and the PR or the issue its body closes
  carries the `agentic-auto-merge` opt-in** — alongside the sync-set and
  open-blocker gates it consults today
  (§forgejo/decisions/merge-gate,
  §forgejo/decisions/sync-set, s-git/maitred#179).
- A lens's own push does not re-fire **that lens**; the other lenses'
  dispatches go on (§forgejo/decisions/loop-prevention).
- The landing-set member test is the same gate over the member's
  delivering PR (§forgejo/decisions/sync-set).

The `trigger-review` label overrides the *trigger*, not the chain: it
re-asks the lenses for the current revision — including one that
already approved, which is the point of the human's explicit request —
while the subject-slot guard keeps the label's mere presence from
re-firing (§forgejo/decisions/label-triggered-review).

## Transition table

| When (event + re-fetched state) | Decision |
|---|---|
| issue opened (open, all blockers closed, no connected PR) | `implement` |
| issue opened (blocked, or a connected open PR, or not open) | hold off |
| issue opened / edited / labels changed / comment not authored by the configured decomposer identity (issue open, carries both the `ideation` and the `teardown-forest` label, blocks at least one issue that is not an `outcome` tracker) | `teardown-forest` — the teardown row wins over the root's `decompose` row, so a forest under teardown is never re-commissioned while it is being deleted (§forgejo/decisions/forest-teardown) |
| issue opened / edited / labels changed / comment not authored by the configured decomposer identity (issue open, carries both labels, blocks no issue other than a tracker) | hold off, naming what the root still blocks — the engine can see there is nothing left to delete, so no agent is asked to discover it, and a tracker standing by the order's own rule is that state (§forgejo/decisions/forest-teardown) |
| issue opened / edited / labels changed / comment (issue open, carries the `teardown-forest` label without `ideation`) | hold off, naming that the label modifies the root row and retires no work item — a kind label carried alongside is named first, since what an issue *is* outweighs what a label *modifies* (§forgejo/decisions/forest-teardown) |
| issue opened / edited / labels changed (issue open, carries the `ideation` label, and — for the labels-changed event — no longer carries `teardown-forest`) | `decompose` — the label makes the root, whatever the repository and the title; attaching the label is the *first* trigger. The event path keeps the revision-keyed watermark guard; the sweep is watermark-exempt (§forgejo/reconciliation/watermark-exemptions) |
| issue comment by an author other than the configured decomposer identity (issue open, carries the `ideation` label) | `decompose` — the comment bumps the root's `updated_at`, so it reads as a new revision under the ordinary revision-keyed watermark guard: a human answering the decomposer's questions re-cures the forest event-driven, without waiting for the sweep; a root that also carries `teardown-forest` takes the teardown row instead, as an edit does (§forgejo/decisions/forest-teardown) (§forgejo/decisions/loop-prevention, s-git/maitred#147) |
| issue comment authored by the configured decomposer identity (issue open, carries the `ideation` label) | hold off, naming the authorship — the decomposer's own comment on its root must not re-trigger it; an unset identity leaves the guard inert (§forgejo/config/decomposer-identity, §forgejo/decisions/loop-prevention, s-git/maitred#147) |
| issue opened / edited / labels changed / comment (issue carries the `outcome` or `human-task` label) | hold off, naming the label — the sweep is where `wrap-up` fires (§forgejo/reconciliation/watermark-exemptions) |
| issue opened / edited / labels changed / comment (issue open, carries the `forest-seeding` label) | hold off, naming the seeding window — a newborn forest member is dispatchable neither for `implement` nor for `reassess` until its blocking edges are wired and the decomposer removes the label (§forgejo/decisions/transition-table, §forgejo/decisions/commissioning) |
| issue opened / edited / labels changed / comment (issue carries the `sync` label) | hold off, naming the label — the sweep is where a landing set's `sync` fires (§forgejo/reconciliation/watermark-exemptions, §forgejo/decisions/sync-set) |
| issue edited / labels changed / feedback comment (issue open, all blockers closed) | `reassess` |
| issue edited / labels changed / feedback comment (issue open, an open blocker) | hold off — the reason names the open blockers |
| issue edited / labels changed / feedback comment (issue not open) | hold off |
| issue closed | unblock cascade |
| PR opened (open, mergeable) | `review` fan-out — every lens, none has an approval at head |
| PR opened (open, not mergeable) | `rebase` |
| PR synced (open, not mergeable) | `rebase` |
| PR synced (open, mergeable) | `review` fan-out — the lenses with no undismissed approval at the new head (§forgejo/decisions/review-lenses) |
| PR carrying the `trigger-review` label (open, mergeable) | `review` fan-out for the current revision — explicit request, overriding the per-lens sender guard and re-asking even an approving lens (§forgejo/decisions/label-triggered-review) |
| review submitted (every lens reported at head, the reports not all approvals, PR authored by the implementer identity) | `fix-feedback` (§forgejo/decisions/review-lenses) |
| review submitted (every lens reported at head, the reports not all approvals, PR authored by anyone else) | hold off — the reason names the PR's author and the configured implementer identity (§forgejo/config/implementer-identity, s-git/maitred#143) |
| review submitted (every lens approved at head, the PR or the issue its body closes carries `agentic-auto-merge`, no connected issue has an open blocker) | `merge-or-wait` |
| review submitted (every lens approved at head, neither the PR nor the issue its body closes carries the `agentic-auto-merge` label, no connected issue has an open blocker) | hold off — the reason names the missing opt-in and the connected issues: an approved PR with no merge opt-in waits on a human (§forgejo/decisions/merge-gate, s-git/maitred#179) |
| review submitted (every lens approved at head, a connected issue has an open blocker) | hold off — the reason names the open blockers (§forgejo/decisions/merge-gate) |
| review submitted (a connected issue carries `agentic-sync-merge`, or the PR itself stands in an open grouping issue's blockers) | hold off, naming the sync set — the member never merges on its own; the member's open sync sets are emitted for a `sync` dispatch against their grouping issues. A member of **no open set** — its set retired — decides on through the merge gate, its label stale rather than a gate; a member whose work also carries `agentic-auto-merge` — on the pull request or on the member itself — holds off whatever sets exist, the reason naming the set it belongs to, the merging opt-in and the seat that opt-in sits in; an opt-in carried by a different work item the pull request merely co-delivers is that item's own and names no authoring error (§forgejo/decisions/sync-set, §forgejo/decisions/merge-gate) |
| review submitted (a lens has not reported at head) | `review` fan-out for the lenses still owed; fix-feedback holds off naming the outstanding lenses (§forgejo/decisions/review-lenses) |
| review submitted (every lens reported at head, comment reports only) | hold off — the sweep is the row that turns comment reports into fix-feedback (§forgejo/decisions/transition-table) |
| PR merged | unblock cascade (for the connected issue) |
| PR closed without merging | hold off |
| PR commented (open) | as reconcile — the sweep's logic: the re-fetched PR state decides the next action — but **not** the sweep's watermark exemption: an approved `agentic-auto-merge` PR whose watermark already records `merge-or-wait` at head holds off here, and so do the states whose subject slots already record the lens `review` at head or whose watermark records `fix-feedback` at head (§forgejo/reconciliation/watermark-exemptions, §forgejo/state/watermark) |
| reconcile — open issue carrying both the `ideation` and the `teardown-forest` label, blocking at least one issue that is not an `outcome` tracker | `teardown-forest`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open issue carrying both labels, blocking no issue other than a tracker | hold off, naming what it still blocks: the re-firing ends where the order has nothing left to delete (§forgejo/decisions/forest-teardown) |
| reconcile — open issue with the `ideation` label | `decompose`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue, no open blockers | `wrap-up`, revision notwithstanding (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue with open blockers, its `ideation` blocker closed | `decompose`, revision notwithstanding — the forest is commissioned and unfinished, so curation continues (§forgejo/reconciliation/watermark-exemptions) |
| reconcile — open `outcome` issue whose `ideation` blocker is **open** and carries the `teardown-forest` label | hold off, as any tracker with an open root does: the teardown order needs no row of its own, and the root's own edge is what keeps a spared tracker from reading as finished (§forgejo/decisions/forest-teardown) |
| reconcile — open `outcome` issue with open blockers, its `ideation` blocker open | hold off, naming the root — the forest is not yet commissioned, and the root's own row is the pulse: a forest is curated once per sweep |
| reconcile — open `outcome` issue with open blockers and no `ideation` blocker | hold off, naming the absence — a tracker not gated by a root is not a forest's tracker |
| reconcile — open issue carrying the `sync` label | `sync` when every member is closed or approved-at-head, mergeable, and opted in; otherwise a silent hold-off naming the members that are not ready — and a hold-off naming both reserved labels when the issue also carries `ideation` or `outcome`, two kind claims one row must not settle by label order (§forgejo/decisions/sync-set) |
| reconcile — the sweep's synthetic current-state event | as issue opened (issue); for a PR, as PR synced — except when the chain has reported at head: **every lens approved at head** dispatches `merge-or-wait` only for a PR whose work is opted into merging — held off, naming the open blockers, when a connected issue has one (§forgejo/decisions/merge-gate), held off, naming the missing opt-in and the connected issues, when neither the PR nor the issue its body closes carries `agentic-auto-merge` (s-git/maitred#179) — and dispatches it **revision notwithstanding** for the opted-in PR, the exempt row of an approved PR at a frozen head — while **every lens reported and the reports not all approvals** dispatches `fix-feedback` **revision notwithstanding**, the exempt row of a PR standing at feedback for a fix that has not landed — behind the ownership gate, so a PR authored by anyone but the configured implementer identity holds off naming the author and the identity (§forgejo/config/implementer-identity, s-git/maitred#143). While any lens has not reported at head, the synced-like fan-out row dispatches those lenses **revision notwithstanding** — the exempt rows of a PR waiting to be reviewed, exempt per lens while the lens has reported nothing at the head (§forgejo/reconciliation/watermark-exemptions, §forgejo/decisions/review-lenses) |

The **merge gate is the only place work is stopped**, and it stops it
by holding off, never by closing: whichever path can dispatch
`merge-or-wait` — the review event, a PR comment, the sweep's PR path,
or the unblock cascade's — reads the connected issues' open blockers
first (§forgejo/decisions/merge-gate) and requires the
`agentic-auto-merge` opt-in on the PR or on the issue its body closes
(s-git/maitred#179), and the
review, rebase, fix and merge rows decide everything else. A pull request opened against work
whose forest has not been commissioned is therefore closed nowhere,
commented on nowhere, and treated no differently from any other pull
request — reviewed or rebased on its own state — while its merge is held
off naming the blocker for as long as the blocker stands, and the
decision log is the engine's only remark about it. Un-commissioned work
is held off upstream, where the `forest-seeding` window gates the
member's `implement` (§forgejo/decisions/commissioning); a branch and a
pull request that a human can read are not what the gate exists to
forbid. The paths that dispatch no merge read no graph at all — the
`PR synced` and `PR opened` events grow no hop, keeping those paths
cheap — and the next review submission, PR comment or sweep applies the
gate (§forgejo/webhook/re-fetch).

A PR's **connected issues** are the issues its body auto-closes and the
non-PR issues it blocks
(§forgejo/webhook/re-fetch). The blocking edges among them are
**recorded** relationships. Forgejo creates no PR→issue dependency from a
`Fixes #NN`
keyword in a PR body, and the engine's client is read-only
(§forgejo/client/operations), so an edge must be recorded when
the PR is opened: the workflow the `implement` prompt dispatches — the
`/issue-implementer` invocation (§forgejo/config/canned-prompts) —
records `issue-block <PR> → <issue>` for each issue the PR closes,
cross-repo targets included, and the engine only reads it. That
instruction lives in the canned prompts, deployed outside this
repository (§forgejo/config/loading); **it does not exist yet**, and
until it does the recorded half of the read contributes nothing. It no
longer decides whether the gate has anything to look at: the auto-close
reference the body carries is read through the forge's own grammar
wherever a merge may be dispatched, so a PR whose opening recorded no
dependency still connects the issues it closes, and their blockers are
the gate's rows (§forgejo/webhook/re-fetch, s-git/maitred#180).

There is no longer a single "latest review" that decides: the
review-following rows read the **chain** — each lens's latest
undismissed review at head, and each author's latest undismissed
review at head (§forgejo/decisions/review-lenses). An
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
decomposer re-cures against, so an edit re-fires `decompose`; so does a
comment, by the same revision-keyed guard — the comment bumps the root's
`updated_at` — except when its author is the configured decomposer
identity, whose own comment holds off naming the authorship, so the
decomposer's clarifying question never re-triggers it
(§forgejo/decisions/loop-prevention, s-git/maitred#147).
A root that is closed on re-fetch holds off on every row: closing it
commissions the forest; the unblock cascade dispatches its members.
The `outcome` and `human-task` labels keep their hold-offs, naming the
label: the `human-task` label marks a ticket claimed by a human and is
never an `implement` candidate anywhere in the engine, the unblock
cascade included (§forgejo/decisions/unblock-cascade). The role model
(§forgejo/webhook/repository-roles) is the load-bearing boundary; this
rule is insurance.

The `forest-seeding` label is a **transient hold-off**, not a kind
claim and not a claim of *never*: the decomposer attaches it atomically
at a newborn forest member's creation, wires the member's blocking
edges, then removes it, and while it stands the member holds off from
`implement` and `reassess` on every path — the opened and activity
rows, the sweep, and the unblock cascade — so no event can dispatch
work past a forest gate that does not yet exist. The label is therefore
recognised by a **hold-off-label check separate from the
never-work-item taxonomy**, called alongside it wherever that taxonomy
is consulted: the two lists carry different semantics for future
labels — *never work item* is permanent taxonomy, *hold off* a
transient gate the decomposer opens and closes. The member WILL need
`reassess` once commissioned, which is why the gate is a label the
decomposer opens and closes rather than the authorship hold-off the
engine once read (s-git/maitred#135). The kind labels and `human-task` are tested first — what an
issue *is* outweighs the window it happens to be seeding in — and
`teardown-forest` after the transient gate. The hold-off reason names
the window — `issue N carries forest-seeding; forest is seeding, not
yet gated` — and the label's lifecycle belongs to the decomposer, the
engine only observing it (§forgejo/decisions/commissioning,
s-git/maitred#125).

The `teardown-forest` label is a **modifier of the root row**, not a
kind label: it makes no issue a root, a tracker or a claim,
and on an issue without `ideation` it holds off naming that it modifies
the root row. The kind labels are tested first, so an issue carrying
`human-task` or `outcome` alongside it is described by what it *is*
(§forgejo/decisions/forest-teardown).
When an issue carries `ideation` alongside another
reserved label, `decompose` wins — unless the issue also carries
`teardown-forest`, in which case teardown wins: a root is decomposed before anything
else is decided for it — a `human-task` claim on a root withholds
`implement`, not `decompose`, since decomposition is precisely the work
a human may have claimed on a root.

Activity on a **blocked** issue — an edit, a label change, a comment on
an issue with an open blocker — has nothing to reassess: the work is
held by the blocker, not by the activity, so the decision holds off and
names the open blockers (each as `repo#number`, cross-repo included).
This is what makes it safe to attach go-ahead labels to forest members
in any order: labelling a blocked issue wakes no implementer. When the
issue becomes unblocked, the cascade — not the activity — dispatches
`implement`.

**Seeding, not authorship, gates a forest member.** A forest member's authorship is not a gate: an open, unblocked member with no connected open PR and no `forest-seeding` label is an `implement` candidate on every path — the opened row, the unblock cascade and the reconciliation sweep alike. The window in which `implement` would be vacuously correct is the creation race, and it is closed by the label, not by the author: the decomposer attaches `forest-seeding` atomically as it creates the member, wires the member's blocking edges, then removes the label (§forgejo/decisions/transition-table, §forgejo/decisions/commissioning). Authorship is the operator's declaration of *who made* the issue, and what an issue *is* — a work item awaiting its gate — is not a property of who opened it; the engine therefore reads the label and never the identity. The corollary is the sweep's role: because the sweep decides as issue opened, it is the backstop for a member whose unblocking delivery was lost, was never delivered, or was dispatched at a revision that cannot recur (§forgejo/reconciliation/the-sweep, §forgejo/decisions/unblock-cascade, s-git/maitred#135). The cascade is un-narrowed, not widened: the closure that removes a member's last issue-side blocker dispatches `implement` as before, whether that blocker is the ideation ticket or a work-order issue.

A reconciled PR whose **chain has reported at head** — every
configured lens with an undismissed review submitted against the PR's
current head (the review's commit ID equals the head SHA), so no commit
has been pushed since the chain spoke — decides through the
review-following rows: approvals across the board dispatch
`merge-or-wait` unless the merge gate holds it off on an open
blocker (§forgejo/decisions/merge-gate) or neither the PR nor an
issue its body closes carries the `agentic-auto-merge` opt-in, the hold-off
naming the missing label
and the connected issues (s-git/maitred#179); reports that are not
all approvals
dispatch `fix-feedback`, a comment report counting on the sweep's row
(the sweep moves the PR along by triggering the implementer, not the
reviewer) where the pure review event waits for a changes-requested
(§forgejo/decisions/review-lenses, §forgejo/decisions/transition-table).
A PR whose chain has not reported at head — some lens silent at the
current revision, or reports the head has moved past — is reconciled
as PR synced: the fan-out row dispatches those lenses, and
fix-feedback holds off naming the outstanding ones
(§forgejo/decisions/review-lenses).

Both `fix-feedback` dispatches that follow a review — the submitted-review
row and the sweep's — sit behind the **ownership gate**: when the
implementer identity is configured (§forgejo/config/implementer-identity)
and the PR's author is not that identity, the action holds off, the
reason naming both. The PR belongs to its author, who may be handling
the review feedback themselves; dispatching the implementer onto it
collides with them (s-git/s-puppet#118, s-git/maitred#143). With the
identity unset the gate is inert, and the review-following rows decide
exactly as before.

## Label-triggered review

The `trigger-review` label is the **explicit request for an agentic review**.
When an open, mergeable pull request carries it, the decision dispatches
the lens fan-out for the current revision regardless of the review state
the transition table would otherwise produce — including lenses that
already hold an undismissed approval at head, whose re-ask is the point
of the human's request (§forgejo/decisions/review-lenses). The label is
an *override of the trigger*, not of the review's content — the
reviewing agents decide the outcome (approve, request changes, comment)
and the merge policy applies unchanged (§forgejo/decisions/actions).

The label restores the pre-engine affordance where a human, by labelling, asked
for a review that the state transitions alone would not have raised. The
dispatch is keyed to the label's presence at the current revision — each
lens through its subject slot — so it does not re-fire the review while
the label is merely present (§forgejo/decisions/loop-prevention,
§forgejo/state/watermark); removing and re-applying the label at a new
revision is how an explicit re-ask is made. The per-lens sender guard
does not suppress a label-requested review — the label is an explicit
request, not a lens's own push.

`trigger-review` is **additive**: it never suppresses the state-driven
fan-out, and it is distinct from the reserved labels
(`ideation`, `outcome`, `human-task`), which gate work items rather than
request reviews (§forgejo/decisions/transition-table).

## Merge gate

`merge-or-wait` is dispatched **only when the work the PR delivers
carries the `agentic-auto-merge` label and no connected issue of the PR
has an open blocker**. The label is the opt-in and the precondition
of the dispatch on every path that can reach it — the review event, a
PR comment, the sweep's approved row, and the unblock cascade's PR
leg: an approved chain with no opt-in holds off, its reason naming
the missing label and the connected issues, so the wait is visible on
the dashboard rather than merely unwitnessed (s-git/maitred#179). A
PR's **connected issues** are the issues its body auto-closes,
resolved through the forge's closing-keyword grammar, and the non-PR
issues it blocks (its dependency rows, read cross-repo where the
connected issue lives elsewhere) — the two relations that connect a
delivery, read as one list (§forgejo/webhook/re-fetch); an issue's
**blockers** are the issues that block it. The go-ahead label sits on
the **work item** and is read from there or from the **PR** delivering
it: either seat authorises merging on its own — it does not authorise
merging **past a blocker**. The two seats of that go-ahead are one
question, asked by one test: the work is opted into merging when the
pull request carries `agentic-auto-merge` **or** the issue it
implements does, matched case-insensitively on either seat. "Implements"
is the relation the pull request itself states: the issue the PR's own
body names with a closing keyword — `Fixes owner/repo#N`, `Closes #N`,
and the rest of the forge's grammar (§forgejo/webhook/re-fetch) — is
what the label authorises, because that is what the label says: the
issue allows the pull requests that implement it to be auto-merged. A
connected issue the body names nowhere — a grouping issue's blocker
row, a co-delivered sibling, a blocking edge into a repository the
body never mentions — is that work item's own authoring and opts
nothing in: the seat is never a relation the PR's author picked in
passing, which is the difference between an operator opting a forest
into merging and a forest opting itself. Where the body names no such
issue the read reduces to the pull request alone. Every row that asks the
question
asks it through that one test — the precondition every path consults,
the sweep's watermark-exempt `merge-or-wait` row, and the landing-set
hold-off's both-opt-ins test alike — so no row keeps a private copy of
it and two paths cannot disagree about the same pull request. The
dispatch's reason names the seat the opt-in was found in, the pull
request or the delivered issue by its `repo#number` reference, so the
decision log distinguishes a pull request opted into merging from a
forest that opted in through its work item
(§forgejo/observability/the-decision-log). The gate is
the merge-path twin of the open-blocker rule that holds off `reassess`
(§forgejo/decisions/transition-table), and every path that dispatches
`merge-or-wait` consults both halves of the gate through one shared
branch (`decideChainReported`): the review event, a PR comment, the
sweep's PR path, and the held PR the unblock cascade unblocks.

The PR's **own entry** in a connected issue's blocker list is not a
blocker: the PR *is* the connected issue's delivery vehicle (the
issue-blocks record or the body's auto-close reference that connects
them makes the PR that issue's delivery), so the test excludes the PR
itself and counts every other open blocker.

A blocked merge **holds off**. The engine closes no pull request on any
path: work that should not land is held from landing, and work that
should not have started is held off upstream, at the member's
`implement` (§forgejo/decisions/commissioning). A PR whose gate closed
after it opened is therefore legitimate work waiting to land — it holds
off silently on the merge path, the hold-off reason names each open
blocker as `repo#number`,
cross-repo included — and the unblock cascade (or the sweep) moves it
when its blockers close.

The gate reads dependency rows of the connected issues; where none are
recorded, the gate sees no blockers and merges as before. The
recording half lives outside the decision function (the workflow the
`implement` prompt dispatches records `issue-block PR → issue`,
cross-repo targets included); until that record exists the gate
cannot fire — it never blocks more than the graph states. Where the
connection is the body's auto-close reference no record is needed for
the gate to see the issue at all: the reference is the record, and the
issue's own blockers still decide
(§forgejo/webhook/re-fetch, s-git/maitred#180).

## Sync set

A **sync issue** — an open issue carrying the reserved `sync` label —
groups changes that must land together: its blocking issues are the
members of a **landing set**, and the engine dispatches `sync` against
the grouping issue when every member is closed, or stands with its
delivering PR approved at head and mergeable while carrying
`agentic-sync-merge`. No new relation is needed: `blocks` keeps its
one meaning — *you cannot proceed while this is open* — and standing
on a sync issue, proceeding means landing the set. The sync issue is
therefore blocked by construction, and the predicate is the unblock
cascade's, read off a different object.

Three reserved labels, each with a distinct job — Go constants, as
the topics are; none appears in engine config:

| Label | Sits on | Meaning |
|---|---|---|
| `sync` | the grouping issue | this issue is a sync issue, a kind like `ideation` and `outcome`: never a work item |
| `agentic-sync-merge` | a member | its PR may land automatically as part of the set, and is never merged by `merge-or-wait` on its own |
| `agentic-auto-merge` | a work item, or the pull request delivering it | the merge opt-in, and the precondition of every `merge-or-wait` dispatch: it says the pull requests that implement the issue may auto-merge, so the issue seat is the issue the pull request's own body closes; either seat is the whole opt-in, and one test reads both; an approved, unblocked PR with neither waits on a human (s-git/maitred#179); once opted in, the merge policy is the prompt's, not the decision function's |

The predicate is **one function** (`considerSyncIssue`), called from
whichever direction the set state moved: from the reconciliation
sweep, as the backstop for changes that carry no delivery; from the
unblock cascade's walk of outbound edges, since a member's outbound
edges already point at the sync issue it belongs to — there the walk
emits the grouping issue only when the dependency rows it already
holds cannot show the set stalled on a member awaiting a human (one
open without `agentic-sync-merge` is enough to know it is not ready,
and the read it saves is the whole set: the grouping issue, its
members, and per open member its PR and that PR's reviews); and from
the approval path, ahead of `merge-or-wait`, so the last approval that
completes a set triggers it without waiting for a sweep — there the
candidate is emitted and the set's own re-fetch decides it, the same
insurance shape the cascade keys pay. An empty set dispatches
nothing: there is nothing to land. Emission is only ever a
*candidate*: the dispatcher re-reads the set and enqueues nothing
unless the row decides `sync`, and the sweep stays the backstop for
any set the walk held back.

`sync` is a **kind claim**, like `ideation` and `outcome`, so one
issue cannot make two of them. Where a grouping issue carries `sync`
alongside either of the others the sweep's row **names the conflict
and dispatches nothing**, whatever the label order: the sweep is the
set's only backstop, and a row that let the other kind branch decide
first would run the wrap-up prompt against a set that then never
lands, or the watermark-exempt decompose every interval. The shape is
the one a member carrying both merge opt-ins already takes. The
candidate path holds the same line, so no path dispatches a fork of
what the row decided.

The engine's hold-off is **silent**: a set that is not ready is not
an event worth a comment, and an unready evaluation leaves nothing on
the wire — one line in the decision log naming the members that are
not ready (§forgejo/observability/the-decision-log). Once dispatched,
the landing prompt's agent holds the remaining checks the engine
cannot make cheaply: merge rights in every repository of the set,
joint conflicts between individually-mergeable members, and the
merge order the sync issue's body names.

The hold-off sits on **every path that dispatches `merge-or-wait`** —
the review event, the sweep's PR path, and the unblock cascade's PR leg
alike — so a member's PR cannot be landed by whichever direction its
approval arrived from; the cascade's leg therefore takes the same
membership read the event's re-fetch takes
(§forgejo/decisions/unblock-cascade).
The approval hold-off fires on **set membership, not on the label
alone**. A labelled member of no open set — the decomposer retired
the set, or a membership edge went away — has no set whose decision
to wait for, and a silent hold-off would strand its approved,
mergeable PR with no backstop; such a stale label is no gate, and
the merge gate decides on as if the member carried no set label. What
the merge gate then requires is the merging opt-in — on the PR or on
the issue its body closes (§forgejo/decisions/merge-gate): a
stale-labelled member's PR with neither waits on a human, its reason
naming the missing label (§forgejo/decisions/merge-gate,
s-git/maitred#179). A member whose work carries `agentic-auto-merge`
alongside `agentic-sync-merge` is the error state the hold-off exists
to contain: it holds off whatever sets exist, the reason naming the set
the member belongs to, the merging opt-in and the seat that opt-in
sits in.

A member delivers its change through the open pull request that
**auto-closes** it: an open pull request of the member's own
repository whose body carries a closing reference to the member —
`Fixes #N`, `Closes owner/repo#N`, the forge's default keyword set,
read as the forge itself reads it
(https://forgejo.org/docs/latest/user/linked-references/). This is
the relation the forge honours by closing the member when the
delivery merges, and it sits wholly on the pull-request side. The
approval path reads the same relation: its connected-issue read
includes the issues the pull request's body auto-closes, so a member
that records no blocking edge stays visible to the hold-off, the set is
emitted on the approval that completes it instead of at the next sweep,
and `merge-or-wait` never lands a set's delivery out of turn — one
relation for a delivery, read one way by the predicate, the hold-off and
the merge gate alike (§forgejo/webhook/re-fetch, s-git/maitred#180). No
blocking edge between a member and its delivery is required,
recorded, or consulted: `blocks` keeps its one meaning — *you cannot
proceed while this is open* — and the edge that would make a pull
request *deliver* an issue is one no implementer records and no
author is meant to hand-author; a predicate gated on it strands
every set in silence (s-git/maitred#171). The instance's configured
keyword set is a known limitation of a read-only client: the engine
mirrors the forge's defaults. A member delivers through **one** open
pull request — the authoring contract the decomposer keeps. Where a
member nonetheless stands with several auto-closing open pull
requests, the engine reads it through the first one in the
repository's listing; it does not gather reviews across deliveries.
An open member without `agentic-sync-merge` is read no further: the
label holds the set off whatever its pull requests say, and the read
the unready member would cost is the read the cascade's gate already
saves.

A pull request that blocks the grouping issue **directly** is a
blocking issue too, and the predicate reads it: the row is a member
whose delivery is itself, held to the same test — open, mergeable,
approved at that head, carrying `agentic-sync-merge` — with its own
state and reviews fetched in place of a delivering PR's. Dropping the
row would be the one set shape where the engine dispatches a landing
past an unmerged delivery rather than naming what stands in the set's
way, which is the stance every other authoring error takes. A merged
PR there is ready, as a closed member is. The hold-off reads the same
edge: the assembly records the set a PR stands in as that PR's own
membership, so a direct member's approval holds off like any other
and emits its set, whichever of the merge-dispatching paths it
arrived on. Nothing is fetched for it — the edge is already in the
grouping issue's dependency rows the merge gate read — and a grouping
issue the PR does not itself block holds nothing over it, so the
presence of a `sync` issue among the connected issues is not by
itself a gate (§forgejo/webhook/re-fetch). A **closed** grouping issue
is no gate either: the set landed or was retired, and with no set
whose decision to wait for the merge gate decides on.

The dispatch's watermark is a **revision derived from the whole
set** — the grouping issue's body, and for each member what
the predicate reads about it: its state, whether it carries
`agentic-sync-merge`, and its delivering PR's head sha, open and
mergeable state, and approval at that head — so the sweep does not
re-fire at an unchanged set state, and any member's readiness moving
re-fires (§forgejo/state/watermark). The members' `updated_at` is
**not** part of it: it records any edit to
any member — a comment, a title fix, an unrelated label toggle — and
a digest that moved with it would run the landing prompt again on an
unchanged predicate, most noisily for a set stalled on a member
awaiting a human. Narrowing costs nothing: the sweep re-evaluates the
set every interval whatever the digest says, so a change that does
move readiness still re-fires within one interval and one that does
not stops re-firing at all. The grouping issue's **body** is part of
it, on the other side of the same argument: the object that digest
describes is the set, whose body names the order the landing prompt
must honour. Its `updated_at` is not, for the reason that argued the
members off — in Forgejo it moves on a comment, a title fix or an
unrelated label toggle, and a ready set whose run reported an error
would otherwise re-run its landing prompt on every touch of the
grouping issue: merge attempts, and a re-report of a condition the
last run already diagnosed. Without the body in the digest that ready
set — a joint conflict, a repository the agent cannot merge in — would
be a fixed point, and the row this section calls the set's backstop
could never re-fire the state it had just confirmed; restating the
order on the grouping issue is the human's lever on such
a report (§forgejo/reconciliation/watermark-exemptions). `sync` is
admissible by the **outcomes
role** of the repository holding the grouping issue, like `decompose`
and `wrap-up` (§forgejo/webhook/repository-roles); evaluating a set
reads PR state in enabled repositories and the sync issue's own state
in the outcomes one — reads cross edges, but no decision is ever
emitted for an out-of-scope repository.

**Authorship belongs to the decomposer**, not the engine: the agent
that curates the forest creates a sync issue when it derives members
that are not deployable alone, retires one whose members have become
independently deployable (delete, never close), and keeps the member
list, the `blocks` edges, and the stated merge order honest. The
engine never creates, edits, or deletes a sync issue; it only
evaluates and dispatches. A member carrying `agentic-auto-merge` — on
the member or on the pull request delivering it — when it joins a set
has the label removed by the decomposer — a member
with both labels is the error state the approval hold-off exists to
contain: it is never merged by `merge-or-wait`; landing is the set's
decision, and the hold-off names the set the member belongs to, the
merging opt-in and the seat the merging one sits in.
Retiring a set therefore includes removing `agentic-sync-merge` from
its former members: the label is only ever a gate while an open set
stands behind it.

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
cascaded key carrying `ideation` or `human-task` holds off, naming
the label: it is decided by the sweep
(§forgejo/reconciliation/watermark-exemptions); a key carrying
`forest-seeding` holds off, naming the seeding window — and the sweep
*is* a backstop for such a member: once the label is gone an unblocked
member is dispatched by the sweep's row like any other work item,
which is what recovers a member whose seeding-window or unblock
delivery was lost (§forgejo/decisions/transition-table,
§forgejo/decisions/commissioning, s-git/maitred#135); it is decided
when the seeding window ends — the label removal is itself a
`labels-changed` delivery, which the activity row turns into
`reassess` once the member is unblocked; a key carrying `outcome`
dispatches `wrap-up`.

A newly unblocked `outcome` tracker dispatches `wrap-up`, never
`implement`: a tracker is never an implement candidate anywhere in
the engine, the cascade included (§forgejo/decisions/transition-table).
Each cascaded key's admissibility is judged by the roles of the
repository **holding the key**, never the repository whose closure
rooted the cascade (§forgejo/webhook/repository-roles) — which is how
a cascade rooted at an issue in a work-item repository can emit
`wrap-up` into an outcomes repository: the cascaded action is judged
against the roles of the repository holding the tracker, whatever
rooted the walk. The walk may also be **rooted at a close in a
role-less repository**: such a repository is inert, not opaque — the
root itself is never acted on (not re-fetched, not dispatched for, not
watermarked), and admissibility is judged on the **blocked** object
alone, exactly as for any other root. This is what makes the cascade
half of the `wrap-up` row reachable when the last blocker lived
outside the role set (§forgejo/webhook/repository-roles); a close that
blocks nothing wakes nothing, and the sweep remains the backstop for
reads that fail. The dispatch re-checks the action against the
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
dispatches — a PR not standing at an approved head, one with no
`agentic-auto-merge` opt-in on either seat, or one whose connected issue is a member
of an **open** landing set, dispatches
nothing and the sweep remains the backstop. The PR leg assembles the
state the merge gate and the sync-set hold-off read exactly as the
event's re-fetch does — connected issues and their membership edges
included — so membership cannot be visible on one path and invisible on
the other: the cascade is the third way a PR is merged and the labels
table's denial holds on it too (§forgejo/decisions/sync-set). A fork of
`merge-or-wait` on this path is **recorded** in the decision log as the
sibling cascade dispatchers record theirs, never swallowed: a hold-off
with no backstop of its own must be diagnosable from the log
(§forgejo/observability/the-decision-log). The cascade dispatches
nothing else for PRs.

The walk itself is label-blind: labelled nodes are visited and
traversed, so graph walking stays independent of what is finally
dispatched; labels are consulted only at emission — at emission the
walk also emits an **open sync issue it visits** as a sync-set
candidate, gated only on what its dependency rows already show: an
open blocker carrying no `agentic-sync-merge` is a member awaiting a
human, which holds the set off whatever its deliveries, and such a
set is not emitted at all (the grouping issue is a permanent
neighbour of its members' forests while the set is authoring, and the
read the candidate pays is the whole set). Any other open sync issue
is emitted and its own re-fetched evaluation decides, an unready set
staying silent (§forgejo/decisions/sync-set).

## Loop prevention

Five mechanisms, in this order:

1. **The machine is terminating.** Each action's side-effect maps to
   the *next* transition, never a re-run of the same one: a review
   does not push commits; a commit push triggers a review; a
   changes-requested review triggers a fix; a fix pushes commits.
2. **Idempotency + watermark.** The same `(event, state, revision)`
   does not re-dispatch the same action: when the watermark already
   records the action at the current revision (the PR head sha, or the
   issue's `updated_at`), the decision holds off. The guard is
   revision-keyed on every path, and for the review fan-out it is
   **subject-keyed** as well: each lens is guarded by its own slot at
   the revision, so three lenses dispatched at one head are three
   dispatches, and a lens already mid-flight at the head is not
   duplicated (§forgejo/state/watermark, §forgejo/decisions/review-lenses).
   The watermark-exempt actions
   `decompose`, `wrap-up`, `review`, `fix-feedback`, and
   `merge-or-wait` are exempt only **on the sweep**, whose rows for
   them decide without consulting the watermark:
   state is their watermark (§forgejo/reconciliation/watermark-exemptions),
   and the agent's silence-on-noop keeps the re-firing quiet. For
   `merge-or-wait` the exemption is narrower still: the sweep's row, and
   only for an approved, unblocked PR whose work is opted into merging —
   the PR or the issue its body closes carries `agentic-auto-merge`
   (§forgejo/decisions/merge-gate) — so
   an approving review never re-fires on its own. The `review`
   exemption is per lens — the sweep's fan-out re-fires a lens only
   while that lens has reported nothing at the head — and the
   `fix-feedback` exemption is the sweep's row alone: a PR waiting to
   be reviewed, or standing at feedback whose fix has not landed, sits
   at a frozen head sha, and each of those states is self-limiting — a
   lens reporting at head ends its `review` exemption, a chain fully
   reported at head ends the `fix-feedback` exemption's re-ask to the
   lenses, a push ends `fix-feedback` — so the sweep re-fires a
   dispatch that went nowhere until the state moves, while on the
   event path these actions stay revision-keyed and a push or a
   review never re-fires on its own.
3. **Targeted per-transition sender guards**, only where an agent's
   *own* action would re-trigger the *same* action: a commit pushed by
   a review lens must not re-fire **that lens** — the fan-out carries
   on with the other lenses (§forgejo/decisions/review-lenses); a
   comment on an open `ideation` root authored by the configured
   decomposer identity must not re-dispatch `decompose` — every other
   author's comment re-cures the forest, which is what makes answering
   the decomposer's clarifying questions work event-driven
   (s-git/maitred#147). The guards that read a *declared* identity —
   the root-comment author guard, the fix-feedback ownership gate of
   mechanism 5, and the per-lens sender guard, which compares the
   payload's login to each lens's configured `identity` — match by
   exact, case-sensitive equality, no case folding, no substring
   (§forgejo/config/decomposer-identity,
   §forgejo/config/implementer-identity, §forgejo/config/review-lenses);
   the guard fires only where the lens has actually reviewed, as the
   retired reviewing-author guard did.
   The guard yields to an explicit request: a PR carrying the
   `trigger-review` label is reviewed whatever a lens pushed
   (§forgejo/decisions/label-triggered-review).
   Cross-persona triggering (a lens's changes-requested leading to
   the implementer's fix) is intended and unguarded.
4. **The seeding hold-off**, where a delivery that is new to a key
   would otherwise dispatch work that is not yet commissioned: the
   decomposer attaches `forest-seeding` atomically as it creates a
   forest member, wires its blocking edges, and removes the label, and
   while the label stands the issue holds off from `implement` and
   `reassess` on every path (§forgejo/decisions/transition-table, and
   s-git/maitred#135, which retired the authorship guard that used to
   sit here). No authorship guard is needed. An author may not be the
   subject of the action the author's own creation would trigger, but
   cross-persona triggering (a reviewer's changes-requested leading to
   the implementer's fix, a decomposer's member reaching the
   implementer once gated) is intended and unguarded.
5. **The fix-feedback ownership gate**, where the implementer would
   otherwise be dispatched onto a pull request it does not own: with
   the implementer identity configured
   (§forgejo/config/implementer-identity), `fix-feedback` fires only
   when the PR's author is that identity — a human (or their bot)
   handling review feedback on their own PR must not be collided with
   an implementer answering the same reviews (s-git/s-puppet#118,
   s-git/maitred#143). The gate covers `fix-feedback` alone: reviewing,
   reporting on, and merging a human-owned PR stay intended, and the
   author's own push does not suppress a review. Unset identity keeps
   the gate inert. This is a PR-side ownership read, not the retired
   issue-side authorship guard (s-git/maitred#136): it asks who *owns
   the pull request*, not who opened the issue.

## Inputs

**Event** — type, the affected object (repo, kind, number), and the
sender's login. The event types are: issue opened / edited / labels
changed / commented / closed; PR opened / synced / reviewed / merged /
closed / commented; and the synthetic `reconcile` event of the
reconciliation sweep. A general comment on a pull request arrives as
`pull_request_comment` — the PR-side analogue of an issue comment — and
decides as reconcile (§forgejo/decisions/transition-table).

**State** — the re-fetched state of the affected object: the issue or
PR itself (an issue or pull request carrying its **author**, the login
of whoever opened it, §forgejo/client/operations); the issue's blockers (with their states)
and its connected open PRs, for the `implement` and `reassess`
conditions; the PR's reviews, for the review transitions; the PR's labels,
for the label-triggered review override
(§forgejo/decisions/label-triggered-review); for PR reviewed, PR
commented, the sweep's PR path and the unblock cascade's PR leg, the
**connected issues** — the union of the issues the PR's body
auto-closes and the issues it blocks, each with its blockers
(§forgejo/webhook/re-fetch) — for the merge gate
(§forgejo/decisions/merge-gate) and the landing-set read
(§forgejo/decisions/sync-set); and, for close events, the blocker-graph
root — the closed issue — with
each node carrying the issues it blocks and the issues that block it.
For a sync issue under the sweep's gaze, the state carries the
blocking issues as **set members**, each with its delivering PR and
that PR's reviews; for a PR whose connected issue is a member, the
membership edges — the open sync issues that issue blocks
(§forgejo/decisions/sync-set).

The function consults **two declared configuration inputs**: the
configured **implementer identity**
(§forgejo/config/implementer-identity), which the engine passes
verbatim on the state and the `fix-feedback` rows alone read
(§forgejo/decisions/loop-prevention, s-git/maitred#143), and the
configured **decomposer identity**
(§forgejo/config/decomposer-identity), passed verbatim on the state as
well and read by one row alone: the comment on an open `ideation` root,
where the author *is* the relation being tested — a human answering the
decomposer re-cures; the decomposer's own comment holds off
(§forgejo/decisions/loop-prevention, s-git/maitred#147). Issue
authorship still gates nothing — #135 stands for work items; a root
comment is a different relation. Every other row decides on state
alone. Both guards are inert when the identity is unset.

**Watermark** — the last dispatch for the object's key
(§forgejo/state/watermark), or none.
