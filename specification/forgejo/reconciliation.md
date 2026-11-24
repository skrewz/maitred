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
as part of handling any admissible event that lands on an object of a forest —
or whose cascade dispatches that forest's `wrap-up`, the event's object being
no member of it (§forgejo/decisions/unblock-cascade) — it writes that one
forest's region once more
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
  left unpublished, and the event's decision is none the worse — and the same
  test is paid before the drawing reads a cascade `wrap-up`'s tracker rows, so a
  forest the cascade reaches through a role-less repository costs a comparison,
  not a read. The repository a drawing is admitted by is the one whose admission
  the pass established — the event's own, on the event path, where the pipeline's
  role test granted it ahead of any read. A pass that established no admission,
  the inert close among them, names none, and every repository such a drawing then
  reaches must be one a role names in its own right. A root the
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
dispatch — it is a re-fire of a state the engine has already acted on.
What makes that safe is the engine's **re-fire bound**: one unchanged
`(action, digest)` — and, for the review fan-out, one unchanged
`(action, lens, digest)` — dispatches at most once per
`reconcile_interval`, whichever path brought the event
(§forgejo/decisions/loop-prevention, §forgejo/reconciliation/scheduling,
s-git/maitred#259, s-git/maitred#260). Every row compares that digest,
and no row compares an object revision; the rows whose declared inputs
the act of dispatching cannot move dispatch at a digest the watermark
records and leave the pace to the bound
(§forgejo/reconciliation/the-state-is-the-watermark). The sweep is safe
to run at any time, and re-running it is a no-op until Forgejo's state
moves — or, on a state that will not move, until the re-fire bound's
interval is spent.

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
paths, and **no row is keyed on an object revision**: the pull-request
rows and the landing set's row fold the facts they name — the head, the
chain at head, the set's members and their readiness — into a digest of
their own, the same quantity every other row compares
(§forgejo/decisions/inputs, s-git/maitred#260). A comment on a pull
request joins that fold on no row: it is no action's declared input, on
any path, and a comment there is addressed to a human
(§forgejo/decisions/loop-prevention).

This is the engine's whole rule, not the rule for a favoured few; the
actions whose state relation emits no delivery — a blocker edge attached
or removed — are simply the ones the rule was invented for, since a digest
of the declared inputs moves when such an edge moves and a timestamp does
not. Event facts — which row fired, who sent it — fold into no digest at
all, so the sweep and the event answer one value for one state
(§forgejo/decisions/inputs). Because the sweep enumerates open objects only and each eligibility
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

## Rows that re-fire at a digest they cannot move

A digest the watermark records leaves the row one question: would
dispatching change a fact this row reads? For one row the answer is
yes — `sync` lands the set whose readiness its own fold states, so a set
the watermark recorded at readiness is a set already landed, and the row
holds off.

For every other row the answer is no, and a hold-off there would be a
fixed point: the state calls for the action, the watermark records the
digest its last dispatch derived, and nothing on either side can move it.
Every row but that one dispatches at a pair the watermark holds and
leaves the pace to the re-fire bound — the same answer on the sweep and on
an event, which is the split the pull-request rows used to be on the
wrong side of (s-git/maitred#260). No row is exempt from the digest and
none is exempt from the watermark: what differs is only what a true
comparison means to the row that made it, and that is settled once in the
declaration, not on the path that arrived.

The unblock cascade's legs are the one place the comparison still
suppresses beside the landing set's: a leg is a declaration that a
relation moved, not a re-read of the whole state, so a leg arriving at a
digest the watermark records does nothing and the sweep stays its
backstop (§forgejo/decisions/unblock-cascade). What the cascade shares
with the rows this section names is the pace: a `merge-or-wait` leg runs
the same decision and the same re-fire bound the event path runs
(§forgejo/decisions/loop-prevention).

| Row | The state that re-calls it | Why the dispatch cannot move the digest |
|---|---|---|
| `review`, per lens | the PR is open, mergeable, and **this lens holds no undismissed review at the current head** — whether no dispatch reached it or the head moved past its last report (§forgejo/decisions/review-lenses) | the lens's own standing at head is the whole of its digest, so a report that lands ends that lens owing one, and a sibling's report moves no lens's digest but the review-following rows' |
| `fix-feedback` | the PR is open, mergeable, every lens has reported at head, and the chain at head holds no approval — a comment report counts here, as feedback the sweep hands to the implementer (§forgejo/decisions/review-lenses) | a push moves the head, a report changes the chain, and the row's own success is a fix push; a run that died left the chain exactly as it was |
| `merge-or-wait` | the PR is open, **every configured lens holds an undismissed approval at head**, no connected issue has an open blocker (the merge gate, §forgejo/decisions/merge-gate), and the issue its body closes carries `agentic-auto-merge` (§forgejo/decisions/merge-gate, s-git/maitred#179, s-git/maitred#228) | an approved opted-in PR sits at a **frozen head** — nothing more is pushed to it — so the chain, the labels and the head are all fixed until the merge lands, and a merge run that died would otherwise never be retried on an event |
| `rebase` | the pull request is open and not mergeable | a rebase that landed moved the head, which is in the digest; a head a dead rebase left unmoved is the same state that asked for it |
| `implement`, `reassess` | the issue is open, unblocked and awaiting its delivery, or edited since (§forgejo/decisions/actions) | an attempt that died before opening a pull request left every fact the fold states as it found it (§forgejo/reconciliation/the-state-is-the-watermark) |
| `decompose`, `wrap-up`, `teardown-forest` | the forest's or the tracker's state the row names — for `decompose`, with its **digest gate** asked first (§forgejo/decisions/actionable-state-digest) | a run that died left those facts as it found them, and the gate is a suppression of its own, asked before the watermark and stated among these inputs when armed (§forgejo/state/forest-digest) |

The `merge-or-wait` row and the fan-out re-fire for the reason a
`teardown-forest` row does: the state that asks for them is one no
dispatch of theirs can move, and — unlike the dependency-edge case that
first needed this rule — the reason is a **frozen head** rather than a
delivery the forge never sends. Recovery latency is therefore one
`reconcile_interval`: the sweep that finds the unchanged digest dispatches
again, and a merge the forge never ran is retried there rather than left
for a push that cannot come (§forgejo/decisions/loop-prevention).

A PR waiting to be reviewed sits at a frozen head by definition — nobody
is pushing to it while it waits — so a row that suppressed at the digest
it recorded would strand a review dispatch that went nowhere. The fan-out
is owed per lens for that reason: **this lens has reported nothing at
head** is what re-calls it, and a report anywhere on the chain moves no
lens's digest except its own (§forgejo/decisions/review-lenses). Recovery
is self-limiting wherever the action can succeed — a lens reporting at
head ends its own fanning, a chain fully reported at head hands the PR to
the review-following rows (§forgejo/decisions/transition-table), a push
ends `fix-feedback`, a merge closes the PR, and the sweep enumerates
**open** pull requests only (§forgejo/reconciliation/the-sweep).

What that costs is steady-state noise, and the bound on it is stated
plainly: a row in this family re-fires on **every sweep while the state
holds**, and nothing in the engine bounds it further than one dispatch per
`reconcile_interval` per unchanged `(action, subject, digest)` — the
engine's own stamp, not the queue's `(action, subject, digest)` dedup key,
which bounds a task's overlap and never the re-fire's spacing
(§forgejo/webhook/the-pipeline). A re-fire that reaches a task while the
identical one is still `PENDING` is squelched by that key — a fact the
queue reports for pending tasks alone, which is what lets a re-fire
recover a lost dispatch rather than be blocked by the dead one — and every
canned prompt's timeout is minutes against an interval measured in hours,
so a re-fire cannot overtake a task still running
(§forgejo/config/canned-prompts). The agent's own silence-on-noop carries
whatever is left (§forgejo/decisions/loop-prevention). An operator
weighing that cost should read the re-firing as the mechanism working,
not as a fault.

A hold-off the state causes always names the state fact that calls for no
action (`blocked by N open issue(s)`, `no configured lens approves PR 9 at
abc`, `set #4 has 1 member(s) not ready`); the `already dispatched at
digest` reason belongs to the one row a digest suppresses — the landing
set's — and states the facts behind that digest rather than a bare hex; a
row the re-fire bound caught says so, naming the bound that spaced it
(§forgejo/observability/the-decision-log, s-git/maitred#259,
s-git/maitred#260).

The digest gate is the exception that stays, and it is a suppression of
its own rather than a pacing rule. `decompose`'s rows ask first whether
anything a decomposition run could act on has moved: the forest's
actionable-state digest at re-fetch time against the digest the store last
observed for that forest (§forgejo/decisions/actionable-state-digest,
§forgejo/state/forest-digest). Unchanged, the row holds off, and a quiet
forest pays the fold — graph and comment reads, no task. The fold is
computed once per gated row, not once per forest: a quiet forest with an
open root pays one digest computation per sweep, and one whose root has
closed pays one per still-blocked tracker whose row the sweep reaches;
moved, the row dispatches, naming the component that moved (a blocker's
state, a member's, the root's, a human comment); never observed, the
first run is commissioned and the observation is stored. The slot is
refreshed with the digest and its sub-digests once the dispatch lands, so
the next sweep asks its question of current values; a dispatch that
failed leaves the stale slot, and the following sweep retries. The gate is
armed wherever a `decompose` row is reachable — the root's row on every
path, the sweep's tracker row — and an issue that reaches no such row
pays no digest reads: a root under a teardown, a kind-conflicting issue
and everything that is not a root or a commissioned tracker costs nothing
extra. This trades recall for cost: a digest that misses something a run
could have acted on is recovered only by a change to a folded fact or by a
manually triggered sweep (§forgejo/observability/triggering-a-sweep). Where
the gate is armed it answers before the watermark can, on every path — an
edge move changes the digest and emits no delivery, so a watermark
comparison beside the gate would suppress the re-curation the gate exists
to allow (s-git/maitred#209). A comment on an open root takes the root's
row under the same gate: a human comment is a folded fact and moves the
digest, while a comment by the configured decomposer identity folds
nowhere and holds off naming its authorship
(§forgejo/decisions/loop-prevention, s-git/maitred#147).

`decompose` has a second row, on the **tracker**, and it is what keeps a
forest alive after its commissioning (§forgejo/decisions/commissioning).
Closing the ideation issue is the commissioning gesture, and it also
removes the root from the sweep's enumeration of open issues: were the
root the only trigger, curation would end the moment the human
commissioned the work — while the work is exactly what reports the
discoveries a forest must absorb (a blocker nobody listed, a member that
turned out to need another, a landing set whose membership has moved). So
an open tracker that is still blocked is decomposed too, **provided its
forest is commissioned**: the `ideation` issue blocking it is closed. An
open tracker blocked by an **open** `ideation` issue holds off instead,
naming that root, because the root's own row is already dispatching the
same curation and a forest is curated once per sweep. A blocked tracker
with no `ideation` blocker at all holds off naming that absence: a
tracker not gated by a root is not a forest's tracker, and the engine
does not guess which root it belongs to
(§forgejo/decisions/transition-table). The test is one hop because the
tracker's own blocker set is the forest, and the tracker is itself a
member: it carries the membership edge from its root like every other
member does. The engine reads that edge as the commissioning gate; it
never walks a graph to find one.

`decompose` and `wrap-up` are not symmetric, and not by oversight:
`wrap-up` waits for the tracker's blockers to close, because wrapping up
means collecting finished work; `decompose` on a root consults no
blockers, because a root is a root whatever blocks it. For `wrap-up` the
sweep stays the only row that fires it — an `outcome` tracker's activity
on the event path is a hold-off naming the label — and the **unblock
cascade** dispatches it too when it newly unblocks a tracker
(§forgejo/decisions/unblock-cascade). A cascade leg applies the ordinary
comparison for its own key, over the issue and the blocker rows it reads
before dispatching, and the sweep stays the state-based backstop for the
member the cascade's read could not see.

## The landing set's row

The sweep's `sync` row is the pull-request path's one row a digest
suppresses. Its declaration names the set: the grouping issue's number,
state, title and body, and per member its number, state, the member
label's presence and its delivering pull request's head sha, open and
mergeable state and approval at that head — plus the event's own issue,
so the event that declares the row is in the pair like every other
row's (§forgejo/decisions/inputs, §forgejo/decisions/sync-set). The
members' and the grouping issue's `updated_at` fold nowhere, so a comment
or a label toggle that moves no readiness does not re-run the landing
prompt; what the fold does carry is the grouping issue's **body** — the
merge order the landing run was told to honour — because a ready set
whose run reported an error is otherwise a fixed point the backstop
cannot fire on. The row is the backstop for set changes that carry no
delivery — an edge edit, a human closing a member, a human restating the
order: the candidate no event fired is caught within one reconcile
interval, and a set whose state has genuinely moved re-fires at once.
