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
2. **Record the forest set.** Enumerate the forests — the
   `ideation` issues of the outcomes repositories, in either state,
   paired with the trackers their roots block — and record the set the
   **outcomes** view is served from; the split into **current** and
   **past** is the rows' own reading of their states
   (§forgejo/observability/the-outcomes-view). This is why the sweep,
   not an API request, pays the view's reads; a failed enumeration
   leaves the view saying so rather than serving a set it can no
   longer vouch for (§forgejo/webhook/the-scope-cache). The set carries
   the moment it was recorded, and the view serves it for at most one
   reconcile interval, so a sweep that returns before this step — the
   organisation listing of step 1 having failed — ages the view out
   instead of leaving it serving the set recorded before the outage.
   It is step 2 rather than a tail to the walk so that the walk, with
   the pipeline's re-fetches in it, keeps the sweep's slow work: a walk
   delays the next recording only by overrunning a whole reconcile
   interval — the ticks themselves belong to the interval as counted
   from the engine's start. Where a walk does overrun one, the set ages
   out while that walk runs, which is the one-interval bound holding
   rather than a hole in it.
3. **Enumerate the objects.** For each such repository, list **all**
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
4. **Decide and dispatch.** For each listed object, feed the synthetic
   `reconcile` event (§forgejo/decisions/inputs) through the **same
   pipeline as the event path** (§forgejo/webhook/the-pipeline):
   re-fetch, decide, dispatch, watermark — serialised on the watermark
   store's per-key lock (§forgejo/state/concurrency). The sweep shares
   the decision function and the event path; no logic is duplicated.
5. **Write the trackers' current-state blocks.** For every **current**
   forest step 2 enumerated, render its graph and land the drawing on
   that forest's tracker; a past forest — its tickets both closed —
   draws nothing, the engine not editing a finished forest's history
   (§forgejo/reconciliation/the-tracker-block). Like step 2 this is
   the tick paying so no request path has to: the chart is at least as current as
   the forest set the outcomes view is served from, the event path's redraw having
   drawn whatever moved between ticks (§forgejo/reconciliation/the-tracker-block). It comes **last** because it
   is the step that changes nothing the tick decides: a forge that has gone
   slow must not put the drawing between the sweep and a decision, and
   nothing downstream needs the block written first — the digest excludes a
   tracker's body and its `updated_at`
   (§forgejo/decisions/actionable-state-digest). A forest whose write fails
   is reported and skipped: it neither fails the tick nor delays a decision.
   The drawings it records replace every picture pair of the current forests
   in the set step 2 published; between the two steps the view serves the
   pairs that set carried across, never an empty one it has drawn no
   conclusion from, and a past forest's pair stands frozen at the drawing
   the forest held when it was last current
   (§forgejo/observability/the-outcomes-view). An event-path redraw that landed
   between the two steps is drawn over here: the tick's drawing reads the live
   graph, and so does the redraw's (§forgejo/reconciliation/the-tracker-block).

The synthetic event carries no sender.

## The tracker block

An outcome tracker's description carries its forest's drawing between a pair of
HTML comments — `<!-- maitred:forest:begin -->` and `<!-- maitred:forest:end -->`
— under a `## Current state` heading. On every tick the engine writes that
region for every current forest it enumerated (§forgejo/reconciliation/the-sweep), and
as part of handling any admissible event that lands on an object of a forest it
writes that one forest's region once more
(§forgejo/reconciliation/the-tracker-block, s-git/maitred#274): it gathers the
graph, renders it
(§forgejo/renderer/the-contract), and splices the
source under a `Last revisited by maître d' engine: <YYYY-MM-DD HH:MM> <TZ>`
stamp, the time rendered in the configured timezone
(§forgejo/config/tracker-chart). The attribution is the point: another author
redrawing the region still writes the plain `Last revisited:` line, so from the
stamp alone — without logs or a diff — a reader can say the engine drew the
picture (s-git/maitred#251). The tick's
rendering is recorded onto the forest set the outcomes view serves too, so the
payload carries the bytes it drew alongside the block they were spliced into —
and carries them even where the splice found no region to land in
(§forgejo/observability/the-outcomes-view): one drawing per forest per pass,
feeding both sinks. No engine path feeds one sink and not the other, so the two
sinks of one forest state the same drawing whenever the engine drew it last — and
a forest whose picture an event moved states that instant in both of them
(s-git/maitred#274).

- **The fence bounds the write.** Only a tracker's description is written, and
  only the bytes between one opener and its closer. The outcome prose around
  them, a member's body and a grouping issue's body are outside the region and
  stay byte-for-byte as found: a grouping issue's body drives a landing
  decision through its digest (§forgejo/decisions/sync-set), so a stamp written
  there would re-dispatch a merge. The digest excludes a tracker's body and its
  `updated_at` for the mirror reason — the engine rewrites that region every
  tick (§forgejo/decisions/actionable-state-digest). The description is read at
  the moment the block is spliced, not before the graph is gathered, so the
  window in which an edit outside the pair could be lost is one request rather
  than a forest's worth of reads; the body-write endpoint carries no conditional
  write, so within that one request the last writer wins and the next tick
  re-reads whatever it landed.
- **The write goes where a role admits it.** Which repository holds a tracker,
  and which repositories the graph's rows live in, is forge data the root's
  blocking edges name rather than configuration, so the tick's one drawing pays
  the admission test every other cross-repository hop pays
  (§forgejo/webhook/repository-roles): the enumeration's own repository is
  admitted by the enumeration, every other only where a maitred role names it. A
  tracker in a repository no role names is neither written nor drawn — the
  tick skips it and neither sink of the drawing receives a chart or a stamp
  from it, the tracker row itself standing — and the log names the repository
  refused at
  Debug, the level the engine's other cross-repository hops use for the
  sentence; a row no role admits draws no node, contributes no title to the
  picture the outcomes view publishes, and has no edges read. The fourth reader
  of the graph — the forest's actionable-state digest — pays the same test: a
  row, a blocker or a comment a blocking edge reaches in a repository no role
  names folds nowhere, so the gate decides on exactly the rows the chart draws
  or leaves out, never on rows it neither draws nor publishes
  (§forgejo/decisions/actionable-state-digest). The fold's root joins its walk
  as the other readers' roots join theirs: admitted by the walk that found it —
  the event path's role test of the event's repository, and, where the sweep's
  digest gate starts the walk at a tracker, the gate's own test of the root
  before the fold reads anything of it. An unknown scope fails closed, naming
  the failure rather than reading as a repository holding no role — in the fold
  as in the drawing, at the first fact beyond the repository already admitted
  (§forgejo/webhook/the-scope-cache).
- **The stamp moves whatever the graph does.** A pass over an unchanged forest
  rewrites the stamp and nothing else. Where the composed body is byte-identical
  to the body read — two passes inside one minute over an unchanged graph,
  whichever of the two writers made them — nothing is written, so neither a sweep
  nor an event bumps a tracker for nothing. Past that
  minute the write does move the tracker's `updated_at`, and a timestamp the
  engine moves itself cannot be what the engine paces itself on: §forgejo/state/watermark
  states the digest that replaces it, and here that means the re-fire bound and
  the queue's dedup key both hold to one unchanged state however often the forest
  is redrawn, while the delivery the write provokes is the engine's own
  handwriting and holds off unread (§forgejo/decisions/loop-prevention). A redraw
  therefore re-fires no agent that the forest's state was not already calling
  for (§forgejo/reconciliation/the-state-is-the-watermark).
- **The write says so.** A tracker whose block landed is named at `Info` in
  the log, as every other side effect the engine has is, so a description that
  changed at an odd hour is attributable to the engine rather than to an agent;
  a tracker whose composed body was already there is named at `Debug`, so
  "nothing needed writing" never reads as "this forest was never reached"
  (§forgejo/observability/the-decision-log).
- **The event path draws the forest it touched.** This block's second call site
  is the pipeline itself: an admissible event whose object **is** a forest — an
  `ideation` root, open or closed — or **belongs** to one, by the membership
  relation every other reader of the graph applies (§forgejo/decisions/commissioning:
  an `ideation` issue among the object's blockers names the forest, and the
  object's own label names it when the object is the root), is redrawn as part of
  handling the event, whatever the event kind (§forgejo/webhook/the-pipeline,
  s-git/maitred#274). Nothing is asked of the decision first — no per-kind test of
  whether this event *could* have moved the picture — because the redraw reads the
  live graph: an event that changed no row composes the bytes already there and
  writes nothing. One drawing feeds both sinks, so the outcomes view's `chart`
  moves with the block instead of catching up at the next tick
  (§forgejo/observability/the-outcomes-view).

  Three events draw nothing, and each for a rule rather than a carve-out. The
  sweep's synthetic `reconcile` event, because the tick that sends it draws every
  current forest it enumerated at step 5 (§forgejo/reconciliation/the-sweep): the
  redraw is the event path's work, not a second drawing per row a sweep walks.
  An event on a pull request, which the drawing gives no node and no edge
  (§forgejo/renderer/edge-classes); what a merge delivers arrives as the issue
  events it causes. And an event on a forest whose tickets are both closed, a
  finished forest's history the engine does not edit — its pair stands frozen
  (§forgejo/observability/the-outcomes-view).

  The redraw stands at the pipeline's tail, after the dispatch and the cascade the
  event woke, for the reason step 5 gives for itself: the drawing is the one work
  no decision waits on (§forgejo/reconciliation/the-sweep). So a `wrap-up` task is
  queued before the block it describes is redrawn — the ordering the dispatch used
  to arrange for itself (s-git/maitred#232) — and the picture the finished forest
  leaves behind still states the graph the wrap-up was dispatched from, the graph
  being exactly what the redraw reads. Membership is a relation, not a label, so
  an object two roots' membership edges reach is drawn twice, once for each forest
  it belongs to; an object no membership edge reaches draws nothing, the
  repository's role being what admits the read rather than what makes a forest
  (§forgejo/webhook/repository-roles).

  Because a blocking edge names whichever repository it likes, the root's
  repository pays the admission test every other cross-repository hop pays
  (§forgejo/webhook/repository-roles) **before** its rows are read: a root
  standing where no maitred role names its repository is drawn nowhere, its titles
  left unpublished, and the event's decision is none the worse. A root the
  pairing reaches no tracker is a forest with no region to land in: nothing is
  drawn, the absence stated at `Debug` the way the tick states it. A failed read
  is named at `Warn` with the forest it could not read — the sweep is the
  backstop, as it is for every read that breaks — and a refused write, an
  ambiguous fence pair and an unreadable graph are the `Error` lines the tick
  already writes for the same failures. A drawing that failed erases nothing: the
  pair both sinks held stands under its own stamp, never dressed in a newer one.
  The delivery the write provokes is the engine's own handwriting, and the
  self-authored edit guard holds it off ahead of the read
  (§forgejo/decisions/loop-prevention), which is what keeps a redraw from drawing
  itself.
- **No fence pair is an empty region, not an absent one.** Where a description
  carries no pair, the block is inserted as the body's first heading and the
  body found follows it in the order it was in.
- **More than one pair is an ambiguity, not a choice.** Which region belongs to
  which forest is not guessable, so the engine writes nothing to that tracker
  and the log names it; a chart landing in the wrong region is worse than a
  stale one.
- **The graph is gathered on every pass, never remembered.** Every writer of the
  block — the tick and the event path alike — gathers the rows afresh before it
  splices. The rows are the root,
  the rows the root blocks — trackers included, the same reading the digest and
  the cascade apply (§forgejo/decisions/actionable-state-digest) — and the edges
  are the blocking edges found between those rows, read as the root's and each
  row's blocks. A row that is a pull request is a delivery, not a member: it
  draws no node and no edge (§forgejo/renderer/edge-classes). A closed row
  carries the close time the forge reports (§forgejo/client/operations), never
  its `updated_at`.
- **The strokes come from the forge's own labels.** `root` takes the colour of
  the `ideation` label on the root, `tracker` the colour of the `outcome` label
  on the tracker (§forgejo/renderer/classes-and-strokes). No label names the
  `member` class or the `closed` dim, so those two travel with the engine as the
  house hexes the drawing was converted from — `#6b7a8c` and `#3fb950` — which
  is the only colouring the engine owns. Where a role label's colour cannot be
  read, nothing is written rather than a chart drawn in invented hexes.
- **A failure is one forest's own.** A read that fails, a graph the renderer
  refuses, a ceiling the source will not fit even collapsed
  (§forgejo/renderer/collapsing) and a write the forge refuses are each logged
  naming the forest and skipped: no partial region is ever written, and one
  ragged forest costs nothing to the others. What a failure does to the
  forest's picture fields in the outcomes payload follows what it broke
  (§forgejo/observability/the-outcomes-view): a failure of the **drawing** — a
  read the gathering needs, a graph refused, a role label colour that cannot be
  read, a ceiling the collapsed source will not fit, a tracker in a repository
  no maitred role admits — leaves **both** fields empty for that tick, while a
  failure of the **write** alone — the tracker read the splice needs, a fence
  pair too ambiguous to write between, a forge that refuses the body — leaves
  this tick's drawing in the payload under this tick's stamp, the description
  keeping the bytes of the tick before.

The block carries no verdict and no instruction, which is why writing it makes
the engine no less a decision loop than a task runner: it changes what a human
sees, never what the next tick decides (§overview/non-goals).

## Scheduling

The sweep runs once when the engine starts, then on a **configurable
interval** — the `reconcile_interval` config value
(§forgejo/config/canned-prompts), positive, validated at load time.
Overlapping sweeps do not run concurrently: a sweep that starts while
another is in flight is skipped. Alongside the schedule a sweep can
also be started by hand from the dashboard
(§forgejo/observability/triggering-a-sweep); a manual start is a sweep
start like any other, so the skip-if-in-flight rule applies to it
unchanged.

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
a state whose digest the watermark already recorded is not a new
dispatch on the issue path — it is a re-fire of a state the engine has
already acted on, and the engine's **re-fire bound** lets one unchanged
`(action, digest)` dispatch at most once per `reconcile_interval`,
whichever path brought the event (§forgejo/decisions/loop-prevention,
s-git/maitred#259). On the pull-request side the rows still compare an
object revision, and the watermark-exempt actions named below — the
sweep's rows for `review` and `fix-feedback`, and every path's row for
`merge-or-wait` — decide without consulting it at all, their re-fire
being bounded in time rather than by the revision. The sweep is safe to
run at any time, and re-running it is a no-op until Forgejo's state
moves — or, on a state that will not move, until the re-fire bound's
interval is spent (§forgejo/reconciliation/the-state-is-the-watermark).

## The state is the watermark

The time an object was last updated is never an eligibility criterion and,
on the issue path, never a suppression key either. Whether an object is
actionable is a predicate over its re-fetched state alone — open state,
labels, blockers, connected pull requests, mergeability, and the review
chain at head — and that predicate is asked **once**, through the action's
declaration, so the sweep and an event holding one state reach one answer
(§forgejo/decisions/actions, s-git/maitred#259). What the row compares
afterwards is the **digest of the inputs the declaration names**
(§forgejo/state/watermark), not the instant the object was last touched:
a digest the watermark does not record is new information and dispatches
outright, and one it does record dispatches too, because no action may
fall silent at a digest it cannot move, leaving only the **pace** of that
re-fire to the re-fire bound — one attempt per `reconcile_interval` per
`(action, digest)`, whichever path arrived
(§forgejo/decisions/loop-prevention). Nothing else may differ between the
paths, and the only rows left keyed on an object revision are the
pull-request rows and the landing set, which name the head sha and the set
revision as their digest and are the next member of this chain
(s-git/maitred#260).

This is the engine's whole rule, not the rule for a favoured few; the
actions whose state relation emits no delivery — a blocker edge attached
or removed — are simply the ones the rule was invented for, since a digest
of the declared inputs moves when such an edge moves and a timestamp does
not. Because the sweep enumerates open objects only and each eligibility
predicate is cleared by the action's own success — a delivered
implementation opens a connected pull request, a rebase moves the head —
re-firing on the state is self-limiting wherever the action **can**
succeed, and a dispatch that died recovers within one interval instead of
stranding at a digest it cannot move.

"Connected" is exact here: a pull request is connected to an issue when
it is among that issue's blockers (§forgejo/webhook/re-fetch), an edge
the delivery itself wires. Where the predicate cannot be cleared by a
successful dispatch, the bound spaces a re-fire that never ends, and it
is honest to say so: an issue whose delivering pull request was closed
without merging is open, unblocked, unlabelled and without a connected
pull request from here to the archive; so is one whose implementation
the agent declined without labelling it; and a delivery that opened a
pull request but wired no edge reads as undelivered whatever happened
to the branch. What stops those loops is the state changing — a human
labelling the issue (`human-task`, §forgejo/decisions/actions) or
closing it — and nothing in the engine: the bound is a rate on one
unchanged `(action, digest)`, never a cap on the number of attempts.

## Watermark exemptions

Dependency edges emit no webhook delivery: attaching or removing a
blocker leaves the issue's `updated_at` untouched. For the actions
whose trigger is such a state relation, a revision-keyed watermark can
therefore suppress a dispatch the current state still calls for —
correct-and-silent when it needs to be correct-and-loud.

That is the whole answer for the issue-path actions, which stand
**outside the exempt set** and have no exemption left to grant: their
watermark is the digest of the inputs their declaration names, an edge
move is one of those inputs, and the re-fire bound paces a state that
will not move (s-git/maitred#259).

- `implement` and `reassess` dispatch on the digest of state, labels,
  blockers and connected pull requests.
- `wrap-up` dispatches on the digest an attached or removed blocker edge
  moves (§forgejo/state/watermark).
- `teardown-forest` dispatches on the digest of its downstream edges, so
  an order whose deletable member arrived with no delivery moves it, and a
  teardown that died half way through re-fires until nothing but a spared
  tracker remains (§forgejo/decisions/forest-teardown).
- `decompose` consults the forest's **actionable-state digest gate** on
  every path, dispatching only when the forest's fold has moved since
  the store last observed it
  (§forgejo/decisions/actionable-state-digest, s-git/maitred#209); the
  engine account the gate folds its comments against is a required key
  (§forgejo/config/engine-account), so no sweep runs without the gate.

What remains **watermark-exempt** is therefore the pull-request side,
and for `review` and `fix-feedback` the exemption is a property of the
**sweep's rows** — the idempotency guard stays revision-keyed on every
other path for those actions (§forgejo/decisions/loop-prevention). For
`merge-or-wait` the exemption is a property of **that action's rows on
every path** — the review event, a PR comment, the unblock cascade's
pull-request leg and the sweep alike — because the state it tests, every
lens approving at head, cannot be answered falsely mid-chain: an
approved PR whose delivered issue carries `agentic-auto-merge` sits at a
**frozen head** once the chain approves, so a revision-keyed guard
anywhere is a fixed point and a merge run that died never recovers on an
event. The `review` and
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

The table holds what is left exempt.

| Exempt action | The state that calls for it (on reconcile) |
|---|---|
| `merge-or-wait` | the PR is open, **every configured review lens holds an undismissed approval at head**, no reviewer's latest undismissed review at head is a non-approval (§forgejo/decisions/review-lenses), no connected issue has an open blocker (the merge gate, §forgejo/decisions/merge-gate), and the work it delivers is opted into merging: the issue its body closes carries the `agentic-auto-merge` label (§forgejo/decisions/merge-gate) — the label is the whole point and a precondition of the dispatch on every path, not just this row: an approved PR whose delivered issue carries no opt-in waits on a human whatever the watermark records (§forgejo/decisions/merge-gate, s-git/maitred#179, s-git/maitred#228). And unlike the other exemptions this one is not the sweep's row alone: an approved, unblocked PR whose work is opted in re-fires on every path, its frozen head being the reason the revision cannot be the watermark (§forgejo/decisions/loop-prevention). |
| `review`, per lens | the PR is open, mergeable, and **this lens has reported nothing at the current head sha** — the report the sweep would ask for has still not happened, whether because no dispatch reached the lens or the head moved past its last one; a lens that has reported at head is exempt no more, its dispatch having landed (§forgejo/decisions/review-lenses) |
| `fix-feedback` | the PR is open, mergeable, **every lens has reported at the current head**, and some reviewer's latest undismissed review at head is not an approval — a comment report counts on this row, as feedback the sweep hands to the implementer — and the head has not moved since (§forgejo/decisions/review-lenses) |
| `rebase` | the pull request is open and not mergeable; a head a dead rebase left unmoved is the state that calls for the rebase again, bounded by the re-fire bound (§forgejo/decisions/actions) |

`ideation` and `outcome` are reserved label names, not configuration.
They match **case-insensitively**: a label name is a user-authored
string, and a label created as `Ideation` must not silently decide as
`implement`. When an issue carries both, `decompose` wins: a root is
decomposed before it is wrapped up.

An issue carrying either reserved label dispatches neither `implement`
nor `reassess` on any path — a protected issue is therefore never handed
to the implementer, however the label arrived. For `decompose` the event
path has rows of its own, and wherever the digest gate is armed they
answer with the gate — the same comparison the sweep's rows make, in
place of any guard of their own, because an edge move changes the digest
and emits no delivery, so a guard on a timestamp beside the gate would
suppress the re-curation the gate exists to allow
(§forgejo/decisions/actionable-state-digest, s-git/maitred#209). The gate
is unarmed only for a kind-conflicting root: its event-path row is paced
by the digest of its declaration's own inputs, the same pacing the sweep's
rows take, while the sweep holds that issue off as a conflict before any
decompose row (§forgejo/decisions/transition-table, s-git/maitred#259). A
comment on an open root takes the same row under the same gate: a comment
a human authored moves the digest, since a human comment is a folded fact,
while a comment by the configured decomposer identity folds nowhere and
holds off naming its authorship
(§forgejo/decisions/loop-prevention, s-git/maitred#147). For `wrap-up`
the sweep stays the only row that fires it: an `outcome` tracker's
activity on the event path is a hold-off naming the label.
The **unblock cascade** dispatches `wrap-up` too, when it newly
unblocks an `outcome` tracker (§forgejo/decisions/unblock-cascade); a
cascade dispatch applies the ordinary guard for its own key, keyed on
the emitted action's declared digest over the issue and its blocker
rows the leg reads before dispatching, and the sweep stays the
state-based backstop that re-fires the wrap-up at an unchanged digest
when, say, a human deleted an edge and no delivery fired
(s-git/maitred#259).

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

The `rebase` row and `merge-or-wait` for a PR whose delivered issue
carries no `agentic-auto-merge` opt-in keep revision-keyed watermarks on
every path: re-dispatching those would re-run real work at a head that
has not moved. The `review` (per lens) and `fix-feedback` exemptions are
the sweep's rows alone; on the event path those actions stay
revision-keyed — the lenses through their subject slots
(§forgejo/state/watermark) — so a push or a review never re-fires on its
own (§forgejo/decisions/loop-prevention). The issue-path actions appear
in none of these sentences: they are keyed on the digest of their
declared inputs on every path, which is what an exemption was a
workaround for (s-git/maitred#259).

The `merge-or-wait` row is exempt for the reason `teardown-forest`
is, and not for the dependency-edge reason that opens this section: a
PR whose delivered issue carries the `agentic-auto-merge` opt-in
(§forgejo/decisions/merge-gate, s-git/maitred#228) — sits at a **frozen head sha** once
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
quiet (§forgejo/decisions/loop-prevention).

A PR waiting to be reviewed sits at a **frozen head sha** — by
definition nobody is pushing to it while it waits — so a
revision-keyed watermark makes it a fixed point the sweep can never
fire on, and a review dispatch that went nowhere never recovers. The
state is therefore its watermark: the sweep re-fires until the state
changes, and every state in the table above is self-limiting wherever
the action can succeed (§forgejo/reconciliation/the-state-is-the-watermark
names the shapes no successful dispatch clears) — a
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

A hold-off on the state — exempt or digest-keyed alike — always names
the state fact that calls for no action (e.g. `blocked by N open
issue(s)`); the `already dispatched at revision` reason belongs to the
revision-keyed pull-request rows alone, so the decision log distinguishes
"already dispatched at this revision" from "state calls for no action"
and, for a row the re-fire bound has caught, names the bound that
spaced it (§forgejo/observability/the-decision-log,
s-git/maitred#259).

What the digest buys on the issue path is the same bounded latency
without the split: an edge change is invisible until the next sweep,
which acts on it within **one reconcile interval** because the digest
moved and no timestamp did. What the exemption buys on the pull-request
side is the same, for the same reason — one `merge-or-wait` re-fire
excepted, which the re-fire bound can push to the following tick, the
sweep's own tick paying the bound too
(§forgejo/decisions/loop-prevention).

What it costs is steady-state noise, and the bound on that is stated
plainly: an exempt action re-fires on **every sweep while the state
holds** — an `outcome` tracker still waiting to be closed enqueues a
wrap-up on each sweep for as long as the state applies. Nothing in the
engine bounds it further —
`merge-or-wait` excepted, whose re-fires the engine's re-fire bound
spaces at one per `reconcile_interval`
(§forgejo/decisions/loop-prevention) — the queue's deduplication
squelches a re-dispatch only while the prior task is still in flight,
and the agent's silence-on-noop carries the rest. That
is the design — the state *is* the watermark, and the engine's job is
only to stop suppressing these actions — so an operator weighing the
cost should read the re-firing as the mechanism working, not as a fault.

`decompose` is where that trade was revised (s-git/maitred#209). Its
rows ask first whether anything a decomposition run could act on has
moved: the forest's actionable-state digest at re-fetch time against
the digest the store last observed for that forest
(§forgejo/decisions/actionable-state-digest, §forgejo/state/forest-digest).
Unchanged, the row holds off, and a quiet forest pays the fold — graph
and comment reads, no task. The fold is computed once per gated row,
not once per forest: a quiet forest with an open root pays one digest
computation per sweep, and one whose root has closed pays one per
still-blocked tracker whose row the sweep reaches; moved, the
row dispatches, naming the component that moved (a blocker's state, a
member's, the root's, a human comment); never observed, the first run
is commissioned and the observation is stored. The slot is refreshed
with the digest and its sub-digests once the dispatch lands, so the
next sweep asks its question of current values; a dispatch that failed
leaves the stale slot, and the following sweep retries. The gate is
armed wherever a `decompose` row is reachable — the root's row on
every path, the sweep's tracker row — and an issue that reaches no
such row pays no digest reads: a root under a teardown order, a
kind-conflicting issue and everything that is not a root or a
commissioned tracker costs nothing extra. This trades recall for cost:
a digest that misses something a run could have acted on is recovered
only by a change to a folded fact or by a manually triggered sweep
(§forgejo/observability/triggering-a-sweep). The `wrap-up` and
`teardown-forest` exemptions are untouched — generalising the gate is
separate work, deliberately not smuggled into this one.

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
