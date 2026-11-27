# Forgejo observability

The engine is observable: a human can follow why the engine did or did
not act, and can see at a glance the issues and pull requests the
engine tracks.

## The decision log

The engine **logs every decision** — the dispatched action plus its
reason, or the hold-off plus its reason — keyed by repo/issue/PR (the
watermark key, §forgejo/state/storage). Reasons are **generated from
the action's predicate**, never composed beside the row that hit it: an
unsatisfied answer names the declared inputs the state did not satisfy,
and a dispatch names the digest its inputs moved to
(§forgejo/decisions/actions). An input the engine
could not read holds the row off in the same sentence whatever asked —
`<input> could not be proven — <why>; the reconciliation sweep asks
again` — so the engine's own failed reads are as readable as a state
that says no, and are never mistaken for one
(§forgejo/decisions/inputs, s-git/maitred#261). The one vocabulary of a
declaration's digest that remains outside a predicate's own words is
**inputs unchanged**, said where a row compares the digest the
watermark records rather than where its predicate answers
(§forgejo/decisions/actions, s-git/maitred#259); the re-fire bound's
hold-off stands outside a predicate too, an engine guard composed after
the decision rather than a digest's word
(§forgejo/decisions/loop-prevention).

The log entry is
**structured**: the key, the event that produced the decision, the
decision (dispatch or hold-off), the dispatched action, the reason, and
the revision the decision was made at are discrete fields, so a human
or a tool can filter the log per object and follow the engine's
reasoning. Where a reason quotes a revision, it names the facts the
declaration's digest folds (§forgejo/decisions/actions), the newest of the
comments `reassess` counts spelled out beside the count of them folded (the
thread being the one input whose facts grow with a work item's age). A fan-out
quotes the digest of the one subject it paces, folded from that subject's side,
so a lens's own standing reads as the state it is rather than as the whole
chain's (§forgejo/decisions/review-lenses). A row the re-fire
bound caught says so and names the bound, and the one row a digest
suppresses — the landing set's — names the set's facts as inputs
unchanged (§forgejo/reconciliation/the-landing-sets-row,
s-git/maitred#261). A log line therefore stays
readable against the forge's history whatever the guard keyed itself on
(§forgejo/state/watermark, §forgejo/decisions/sync-set, s-git/maitred#259,
s-git/maitred#260). The field holds the dispatched action's declared digest, so
the line and the watermark file state one value — a fan-out's dispatch line
names each dispatched subject's own digest beside its identity, each the value
that subject's slot holds, since no fold over the whole chain stands in any
slot, key or stamp of that dispatch; a hold-off names no action to
derive a digest from and names the object's own revision, which is what a reader
can place in that history.

The event path and the reconciliation sweep log their decisions alike:
the sweep feeds the same decision function through the same pipeline
(§forgejo/reconciliation/the-sweep), so every decision is logged exactly
once, wherever it is made. A cascade hold-off logs the object it names
as the blocker graph showed it, so its state and revision fields carry
the tracker's own values rather than blanks — an unrelated close event
must not blank the row of an object it did not decide
(§forgejo/decisions/unblock-cascade).

## The dashboard view

The web dashboard gains a **view of the tracked issues/PRs**: the
issues and pull requests the engine has made a decision on. For each,
the view shows:

- the **current state** — the object's state as last re-fetched (open
  or closed; merged, for pull requests),
- the **watermark** — the object's last *singleton* dispatch: the
  action and the revision it was dispatched at
  (§forgejo/state/watermark), or none when nothing has been
  dispatched through the main slot. That revision is the declared digest
  on either side of the engine: an opaque fold of the state rather than a
  timestamp a reader can place in the history, made readable by the reason
  beside it (§forgejo/state/watermark,
  §forgejo/observability/the-decision-log, s-git/maitred#259,
  s-git/maitred#260). Fan-out dispatches do not touch
  the main slot; the view lists the watermark's **subject slots**
  alongside it — one row per dispatched action and revision, naming
  the subjects (the review lenses) dispatched at it — so a fan-out is
  visible even where no singleton dispatch preceded it, and a
  stale-looking main slot is explainable at a glance, and
- the **last decision** — the most recent dispatch or hold-off, with
  its reason and the event that produced it.

The view is served by a dashboard API endpoint whose response is an
object carrying three things: the tracked list, the engine's catalog of
**sweep operations** (§forgejo/observability/triggering-a-sweep), and
**one engine-status field** rendering the engine's state in the words
of the category enum
(§forgejo/observability/why-the-engine-is-off) — while the engine is up
the view's own word for it and no category, while it is off the
fault's category, and when nothing is configured the not-configured
word. The field carries no raw error text: that keeps going to the log
(§forgejo/observability/why-the-engine-is-off). The state is handed to
the web layer at startup as the sentinel classification produced it;
the view never guesses it from a nil engine.

The dashboard renders the view as a section that appears when there is
at least one tracked issue or PR, and — when the state names a fault —
names that category, so a configured-but-broken engine never looks
like a healthy one with nothing tracked. The rows are grouped
**by-repo**: each tracked repository forms a section introduced by a
heading carrying an anchor id derived from the repo's full name, and
under each heading two **tabs** — issues and pull requests — switch
between the rows of each kind. The active tab is the operator's
choice and survives the dashboard's periodic re-render; a repository
with nothing tracked of the shown kind says so in the panel rather
than hiding rows. Each row's **watermark** and **reason** are
click-to-open: collapsed by default, opened by a click, and rendered
so embedded newlines appear as line breaks. The dashboard re-reads
the view on a timer and rebuilds it — the sweep controls with it —
only when what it read differs, so the operator's focus, scroll
position and text selection inside it outlive a refresh rather than
resetting on it. The tab choices and opened cells are state of the
view: they are forgotten when a successful read shows a row or a
repository has left tracking, and forgotten by nothing else — a read
that failed is not evidence that nothing is tracked, so it forgets
none of the view's state. A failed read does render the empty tracked
set, as it did before this restructure: the section blanks while
reads fail, and the rows return with their state on the next
successful read. The restructure hides
nothing that was visible before — repo, kind, number, state, the
watermark's slots, the last decision and its reason all remain
reachable. The view reflects the engine's in-memory record of
decisions: the tracked list is empty when the engine is disabled or
has made no decisions yet, and it is cleared on restart (the
watermark store is unaffected, §forgejo/state/persistence).

## The outcomes view

The forest is the unit of work this engine stewards: an ideation root and
the outcome tracker it blocks (§forgejo/decisions/commissioning). The
engine therefore offers an **outcomes view**: the forests of the
organisation in the repositories holding the outcomes role
(§forgejo/webhook/repository-roles), split into the **current forests**
and the **past** ones. A forest is **current** when its ideation root or
its outcome tracker exists and is open; a forest whose root and tracker
are both closed is **past** — the pair of tickets still identifies it,
and the page shows it under the past tab rather than dropping a forest
that finished while nobody was watching. A root with no tracker is
current while it is open and past once it closes. The view introduces no
new label vocabulary:
it reads only the reserved `ideation` and `outcome` names
(§forgejo/decisions/actions).

The pairing is found the way the cascade finds connected issues: for each
ideation issue in either state the engine asks which issues it blocks
(§forgejo/client/operations) and takes the first blocked issue the
engine reads as a **tracker** — one carrying `outcome` and not
`ideation`, the one reading the cascade and the teardown apply
(§forgejo/decisions/unblock-cascade, §forgejo/decisions/forest-teardown).
A blocked row carrying both kinds is another forest's root, not this
one's tracker: decompose wins over wrap-up wherever both apply, here as
everywhere else in the engine. A root whose tracker is missing — it
blocks no tracker — is listed anyway, **with the tracker stated as
absent**: a forest without its gate is exactly the situation a human
needs to see, so the view never papers over it by dropping the row. An
`outcome` issue that no ideation issue blocks is not a forest and is
not listed. A root whose blocks read answers `404` — the forge's
definitive "no such issue" — lists no forest: the root was deleted
between the listing that made the row and the read of it, and the
teardown order closes roots rather than deleting them
(§forgejo/decisions/forest-teardown), so this is race cover rather than
the steady state. The same `404` is also what a row the token cannot see
answers, so the row is skipped with a line naming the forest that
skipped (§forgejo/webhook/re-fetch); every other failure of the walk
still fails the whole enumeration. A repository whose **every** root
answers that `404`, and whose listing named more than one root, fails
the enumeration rather than listing no forest: the teardown closes roots
rather than deleting them, so a repository answering *absence* for every
root its own listing named is reporting a read that stopped answering,
not a forest set emptied — where one deleted root among live ones is the
race the skip covers. The guard's floor is that evidence: one `404` says
nothing about a set, so a repository whose **only** root answers it keeps
the skip, which is the deleted row s-git/maitred#268 was filed against.
The walk reads one stateless listing of each outcomes repository — every
issue of it, open and closed — and keeps each issue
number once, so a forest whose root closed while its tracker stayed open
is still enumerated, one whose tickets have both closed is enumerated as
past, an issue that changes state mid-walk cannot arrive on both sides
of the split, and a row the paging hands twice because the ordering
shifted is one forest rather than two. Per
tick the widening costs one full issue listing per outcomes repository
— a paging the repository's whole history grows — beside the one
blocks read per forest row the view already paid; the cost paragraph
below carries the numbers. A forest's warnings are walked for the
current forests alone — walking the dead would buy the past tab reads
the current one does not pay — and a past row says so rather than
claiming the empty: its warnings list is empty beside
`warnings_walked` reading false, because the honest empty belongs to a
walk that found nothing, not to a walk that never happened
(§forgejo/observability/the-warnings-a-forest-carries). The view is a
picture of what exists, not of what an order is about to do: an
`ideation` root already carrying `teardown-forest` or
`sync` — a forest under teardown or mid-sync — is still among the
current forests, whatever the decompose gate makes of the same row
(§forgejo/decisions/forest-teardown, §forgejo/decisions/sync-set).

The dashboard API serves the view at `GET /api/forgejo/outcomes`. The
set it serves is the one the last sweep tick recorded: like the tracked
view it answers from memory, and a request costs no forge read
(§forgejo/observability/the-dashboard-view). One acquisition of that
set answers both lists together, so a response cannot straddle two
ticks and name a forest in both halves or in neither. The response is an object
carrying three things: the **forest list** — the current forests —, the
**past forest list** beside it, spelled the same way and never a second
shape, and **one engine-status field** rendering the engine's state as
the forgejo view does (§forgejo/observability/the-dashboard-view), so a
configured-but-broken engine never reads as a healthy one with nothing
to show. Each forest
names its **repository** — spelled as the forge's listing spells it,
falling back to the scope cache's lowercased key where a listing row
omits it — its **root** — number, title, state, repository, and HTML URL
— and its **tracker** — the same fields, naming the repository the
tracker itself lives in, a blocks row listing none living where the
root does, so a tracker blocked across a repository boundary says where
it lives rather than inheriting the root's address
(§forgejo/client/operations) — or the honest absence of one, spelled as
a `null` tracker so the field is always present. Each forest carries the
**picture of the forest** alongside its rows as two further fields, both
always present: **`chart`**, the Mermaid source the engine last drew from
the forest's graph, and **`chart_revisited`**, that drawing's recency
stamp — a pair, drawn as described below, that always travels as one —
and its
**warnings**, the ⚠️
marks saying whether the forest is waiting on a person, an empty list
when it is not, beside the
**`warnings_walked`** flag stating whether that list was walked at
all — true for the current forests, false for the past rows, whose
empty says "not walked", never "nobody waits"
(§forgejo/observability/the-warnings-a-forest-carries). Without the
engine, or
with no forests, both lists are empty — never `null`: an enumeration that
found nothing is an honest empty, not a view that never read. A view the
engine cannot vouch for — no sweep's enumeration has landed yet, the
most recent one failed (the organisation listing, an issue listing, or a
blocks read), or the set it holds has aged past one `reconcile_interval`
without a sweep having recorded a newer one — answers `500` rather than
an empty list or a stale set, because a failed read must not read as an
empty set of forests and a set no longer vouched for is never served as
if fresh: the same rule the scope cache applies to stale snapshots
(§forgejo/webhook/the-scope-cache). The bound is the view's own, applied
as the set is read, so a sweep that never reached the step that records
the set — its own organisation listing having failed — ages the view out
rather than leaving it serving what it recorded before the outage. The
sweep records the set at its head, as its step 2
(§forgejo/reconciliation/the-sweep). Only the honest empty answers `200`
with an empty list. The `500` carries a fixed sentence and the read's
detail goes to the log: raw forge error text carries repository names
and forge response bodies a caller need not be entitled to, and this
port authenticates nothing
(§forgejo/observability/why-the-engine-is-off). A method other than GET
answers `405`.

**Drawing the picture.** The picture is not scraped out of any
description and it is not composed per request: the engine draws it from
the forest's graph with the engine's own renderer
(§forgejo/renderer/the-contract) — the sweep tick once for every current
forest it enumerated, and the event path whenever an admissible event lands
on an object of that forest or its cascade dispatches that forest's
`wrap-up` (§forgejo/reconciliation/the-tracker-block,
§forgejo/decisions/unblock-cascade) — and that one drawing is the
payload's `chart`. The same drawing is also what splices into the tracker's
current-state block, so the payload and the tracker cannot disagree
about what the graph looks like: the chart matches what the block would
render from the same edges (§forgejo/reconciliation/the-tracker-block) —
and it matches even where the write into the description is itself
refused. A description with no fence pair, two fence pairs, or a pair
holding no fenced Mermaid block says where the block may land; it says
nothing about the payload; the past rows' pictures are the frozen pairs
they carried into the past tab (see **The pair travels as one** below).
The outcomes path reads no description and
parses no prose at all: the tracker's description is out of the
picture's chain of command entirely.

The drawing is the renderer's source, **verbatim** — never re-indented,
repaired, or completed in code — and rendered source carries no `click`,
no `subgraph` and no comment (§forgejo/renderer/the-source). What the
source embeds is what the graph's rows report: issue titles and forge
URLs inside labels, and those strings are written by everyone who may
open an issue, mirrored onto a port that authenticates nothing
(§forgejo/observability/why-the-engine-is-off). So anything drawing
`chart` treats it as untrusted input: render it at a non-scripting
security level, or show it as text and never as a graph.

**`chart_revisited`** carries the moment of that drawing in the shape the
current-state block's stamp line carries, `<YYYY-MM-DD HH:MM> <TZ>` — the
rendering instant, spelled in the configured timezone
(§forgejo/config/tracker-chart), the same instant the block's stamp
states for that drawing. It is a string the view does not interpret. The
recency is honest because it belongs to the drawing: the payload's
picture carries the stamp of the pass that rendered it — a tick's or an
event's — not the moment
some agent last wrote a description, and no stamp is ever dressed over a
remembered picture — the pair is written together or erased together, so
a forest a tick drew nothing for carries **both** fields empty once that
tick is done, never yesterday's chart with today's stamp.

**The pair travels as one.** The sweep records the forest set at its head
and makes its drawings after the walk, at step 5
(§forgejo/reconciliation/the-sweep), so the two halves of a tick would
otherwise leave the view answering every forest's picture empty for as
long as that walk runs. It does not: the set the head records carries
each forest's **previous** pair — the last drawing the engine recorded
and the stamp belonging to it — and step 5 replaces every pair in it.
Inside that window the view therefore shows the previous tick's drawing
under the previous tick's stamp, which is what makes an empty picture
honest rather than a half-written record; a forest the head records for
the first time carries both fields empty until step 5 draws it. A forest
that has gone past draws nothing: step 5 addresses the current forests
alone — a past forest's tickets are both closed, and the engine does
not edit a finished forest's history, while a current forest whose
tracker closed with its root still living has that tracker rewritten
as any current row's — so a past row carries the pair the forest held on the
tick it was last current, frozen under the stamp belonging to that
drawing, and a past forest the engine never saw while it was current
carries both fields empty. A carried
pair is never the view's last word: step 5 states what this tick drew,
and an event that touched the forest between the head record and step 5
states itself there too — every drawing the engine makes feeds this field
and the block together (§forgejo/reconciliation/the-tracker-block), and
the carry redraws nothing of its own. The
carry pays the admission test the drawing pays
(§forgejo/reconciliation/the-tracker-block), of both the repository the pair
was drawn from and the one the tracker now stands in — the tracker is forge
data, so a re-pairing can move a picture across repositories, and a role
withdrawn from either empties both fields at the next tick's head
rather than leaving the earlier drawing on the port for the length of the walk.
Where that test cannot be answered — a repository listing the carry fails over
— both fields go empty as well: a drawing whose remit the engine cannot vouch
for is not one the unauthenticated view may publish
(§forgejo/webhook/the-scope-cache), and the failed read is logged as the
failure it is rather than as a refusal.

An empty field is therefore a statement about the engine's drawing,
never about anyone's prose. A forest whose tracker is absent publishes
both fields empty and reads no further (§forgejo/client/operations). A
forest whose **drawing** failed — a read the drawing itself needs (the
root's row, a row's blocks), a graph the renderer refused, or a tracker
in a repository no maitred role admits — publishes both fields empty
too; the failure is one forest's own, the tick logs it
naming that forest, and the others cost nothing
(§forgejo/reconciliation/the-tracker-block, §forgejo/webhook/repository-roles).
A drawing that was made but found no body to splice into — a fence pair
too ambiguous to write between, a tracker read that broke after the
drawing succeeded, a forge that refused the write — still stands in the
payload, since it states what the graph holds even where the description
keeps the bytes of the tick before
(§forgejo/reconciliation/the-tracker-block).
What once emptied a picture — a ragged tracker description — empties
nothing now: a forest whose chart was never written to its tracker
carries the tick's drawing from the graph all the same.

The enumeration happens on the reconciliation sweep's tick, not on the
request path (§forgejo/reconciliation/the-sweep): per tick it costs one
stateless issue listing per outcomes repository — every issue of the
repository, open and closed, which a long history grows — and one blocks
read per ideation root whatever that root's state, on top of the sweep's
own walk. The past list reaches exactly as far as that listing does:
every forest whose tickets are both closed, and nothing bounds it, so a
deployment pays for its own history in the size of the payload and the
height of the past tab rather than in any read the current view does not
already pay — no count caps the list, and none is implied. The
enumeration itself adds no read for the picture, which the tick's step-5
drawing makes for
both of its sinks (§forgejo/reconciliation/the-tracker-block,
§forgejo/reconciliation/the-sweep). The
sweep's own organisation listing keeps the scope cache fresh, so the walk
adds no listing of its own (§forgejo/webhook/the-scope-cache). That walk
is the view's own reader rather than a pairing folded into the sweep's fourth
step: the sweep's listings cover every repository holding either role,
and the blocks reads the digest makes happen inside the pipeline, where
their rows are summed into a digest and discarded, so folding the
pairing in would thread forest rows through the decision path to save
little. A manual sweep start refreshes the set too
(§forgejo/observability/triggering-a-sweep).
What the endpoint answers is therefore always the set the last sweep
tick enumerated, and a request to it — however often a later
`/outcomes` page polls on a timer — costs no forge read at all: the
poll interval becomes a freshness choice, not a cost one. The payload
still enumerates the organisation's ideation roots and trackers to an
unauthenticated port, and the warnings below add the pull requests whose
merges have gone stale and the tickets a human has claimed to what that
port reveals — the same audience, a little more of it, and the addition
carries text its authors wrote. The control on that disclosure is the
port itself and nothing in the code: the server binds every interface
and authenticates nothing, so what reaches the view is exactly what the
deployment's network reaches, and a deployment that publishes the port
beyond its operator's network publishes the payload with it
(§forgejo/observability/triggering-a-sweep, README Web Dashboard).

The dashboard serves the view as a page at `/outcomes` — a route on
the existing SPA, whose fallback already answers the path, so the
route costs the server nothing new. The page lays its rows out across
the full width of the viewport rather than in the dashboard's centred
column: the widest thing on the page is a forest's picture, and the
reader's screen is the room it needs (s-git/maitred#249). The page
polls the endpoint on a timer, turns the **Current forests** heading
into a pair of tabs — **Current forests** and **Past forests**, each
carrying its count — and renders **one row per forest** of the tab the
reader chose: the repository the
forest grew in, the ideation root as a link carrying its state, and
the tracker as a link carrying its state — or the absence of one,
stated in the row rather than dropped. The row draws the forest's
picture underneath its links — see **Drawing the picture** below — and
states the region's recency stamp beneath it, verbatim, the timezone
abbreviation being the tracker's to choose rather than the page's.
The link is only a
link when the forge's URL is one a link can carry (http or https);
anything else renders as text, since the port authenticates nothing.
The row's shape is the same on both tabs; only the payload's lists
differ. A forest whose ideation root the payload says is **open** is
**uncommissioned** — commissioning being the root's closure
(§forgejo/decisions/commissioning) — and its row renders with a
greyed-out look: one presentation rule on the row mutes links, badges
and picture alike, and the dim rides on the row alone, so a picture
enlarged from a dimmed row draws in full colour. The muting says *not
commissioned*, never *fault*: warnings keep their ⚠️ entries on the
dimmed row, and the dim never sinks below half opacity, so an entry
stays readable. A stalled forest whose root has closed wears no dim, so
the two read apart at a glance (s-git/maitred#285); a forest under
retirement greys alike, retiring one being the human reopening its root
(§forgejo/decisions/forest-teardown). A forest never commissioned has no
tracker, and where there is no tracker the tick writes no block and
draws no picture (§forgejo/reconciliation/the-tracker-block), so its
description has nothing to grey; where an open root does carry a
tracker, the sweep redraws that description every pass and the renderer
dims closedness alone (§forgejo/renderer/classes-and-strokes). A
forest the payload says is waiting on a person draws its **warnings** in
the row: one ⚠️ entry per warning, each naming its kind, the issue
or pull request holding the forest up, and the age of the condition,
and linked through the warning's own HTML URL under the same link test
the row's other links pay
(§forgejo/observability/the-warnings-a-forest-carries). The page
composes nothing of its own: an entry's reference, kind and age are
the payload's fields and nothing else, so what the row marks is what
the walk found and not a second judgement. A forest that carries no
warnings carries no marker — a forest that is merely big is not a
forest that is waiting. Where there are no rows the page says which
of the two states it is in, in the engine's own status words: an
engine that is not up names
its state (the category word the enum prints,
§forgejo/observability/why-the-engine-is-off), and an enabled engine
whose last sweep found nothing says the tab it is showing holds no
forests.
A view the engine answers `500` for says it could not be read, so an
empty rectangle never passes for a dashboard
(§forgejo/observability/the-dashboard-view). The header links to the
route so it is reachable by eye; it carries one link, which reads as
the way back when the route is where the reader already is. The
header's trigger statistics — numbers this page never reads — stay
hidden on the route, so they state nothing rather than rendering
their defaults as facts.

**Drawing the picture.** A row's picture is the `chart` field, and the
page draws it as Mermaid: the source is handed to a renderer at the
non-scripting `strict` security level, because the text a tracker's
region carries is whatever a writer left there and the port
authenticates nothing, and a drawing that ran `click` or `link`
callbacks would let that text execute in the viewer
(§forgejo/observability/why-the-engine-is-off). The source reaches the
page as escaped text and never as markup, so a source carrying one is
rendered as the text it is rather than as an element.

The renderer is **vendored** into the page's own asset directory and
served by the server itself: the container has no reason to reach the
public internet to draw itself, and a publicly mirrored repository
whose build depends on a third-party host is worse than a large
vendored file (s-git/maitred#191). What is vendored is
`mermaid@12.3.0`'s `dist/mermaid.min.js` — npm's released artifact for
that version, 5,537,838 bytes, sha256
`336df803d87a38b70a01e736e666c8f7601d2ce42f5e47c8fafe5d721074b9a3` —
and it stays byte-identical to that artifact: no re-save, no
reformatting, no filter. Refreshing the runtime means replacing the
file with a named release's artifact and restating this sentence to
name it, because `TestVendoredMermaidRuntimeIsServed` compares the
embedded bytes against the digest recorded here and fails on any other
byte. The page loads no script from anywhere but its own origin — a
page that could not reach a CDN still draws. Every response this port gives carries a
content-security policy that admits no origin but the server's
(`default-src 'self'`, images from the origin or `data:` only, no
object, no submission target, no foreign base) and the mark that no
type may be sniffed from a response, so neither the page nor a drawing can make
the viewer's browser reach a host the server itself does not reach, or
reach it on a form or a rewritten base. What that policy does not close
is inline execution: it admits the page's own inline script, styles and
attribute handlers, and the theme CSS Mermaid injects as it draws, so
it stands against every origin but this one and not against the page's
own code.

Where the source will not parse the row states the source itself as
text rather than nothing: an unreadable drawing beats a missing one,
and the refusal leaves no error drawing of the renderer's own anywhere
on the page. The same holds when the runtime itself never arrived: a
page whose script did not load states every picture as text rather
than blanking the rows. A forest carrying no picture builds no chart
cell and states no stamp, since the payload's picture and stamp are one
pair (see **The pair travels as one** above).

Clicking the drawing — or pressing Enter or Space with the picture
focused — opens a **full-screen overlay** of the same drawing: the
copy the reader asked for, not a second rendering, and a copy that
carries the row's fallback mark with it, so an enlarged fallen-back
picture shows the same text the row shows rather than an empty panel.
Escape or a click
outside the drawing closes it, and a click of a picture still drawing
opens the overlay once the draw settles rather than empty — and opens
nothing at all if a render replaced the list while that draw was in the
air, since the picture it would show is no longer on the page. Every
re-render — including the renders that replace the list with an empty
or unreadable view — closes the overlay rather than leaving it showing
a picture the next poll has replaced.

### The warnings a forest carries

A forest waits on a person in exactly two ways, and each forest of the
payload carries a **warnings** list saying which — and nothing else.
Each warning is a reference (repository, number, title and HTML URL, the
same four fields a forest row carries and for the same reason, since what
holds a forest up may live across the organisation), its **kind**, and
the **age** of the condition in seconds, so the dashboard links to the
object rather than constructing the judgement from reads of its own. A
forest nothing is waiting on carries an empty list, never `null` — the
honest empty the forest list itself is, `warnings_walked` true saying
the walk ran and found nothing; a past row's empty carries the flag
false, stating a walk that never happened rather than a forest nobody
waits on (§forgejo/observability/the-outcomes-view). A warning reports; it dispatches
nothing, holds nothing off and gates nothing, and no decision reads a
warning back (§forgejo/decisions/actions).

- **`stale_agentic_merge`** — an open pull request delivering an open
  member that opted its landing in, through `agentic-auto-merge` or
  `agentic-sync-merge`, at the shape the merge gate fires at once the
  forest's own rows are set aside, and holding nothing else off
  (§forgejo/decisions/merge-gate, §forgejo/decisions/sync-set): still
  open, still mergeable, approved at head by every lens with no
  non-approval above it, no open blocker of any issue the delivery
  connects — those its body names and those its blocking edges name —
  ahead of it, and no open landing set whose landing it waits on.
  That question is the merge gate's, so it is asked the gate's way:
  through the connected-issues read the gate reads, whose open blockers
  the gate names and whose landing-set membership the gate names
  (§forgejo/webhook/re-fetch). The view keeps no second definition of
  "stale" for a change to the gate to strand — a warning that asked its
  own list of rows would be a promise two files had to keep alike — and
  each of those holds is the gate waiting on work rather than on a
  person, so the ⚠️ that asserts a human is the remaining wait says
  nothing. Two answers the gate path forces on a merge are turned the
  other way here, because the view has no merge to protect and an
  unchecked wait is not asserted: a reference or blocking-edge row into a
  repository the remit will not admit, and a graph reaching past the read
  budget, hold the warning back rather than failing the view
  (§forgejo/webhook/repository-roles, §forgejo/webhook/re-fetch). What
  the screen admits is resolved as the gate resolves it, so the answers
  the forge itself ignores — a dangling reference, a reference naming a
  pull request — are ordinary here too, connecting nothing rather than
  answering an outage. The forest's own rows are the one carve-out,
  under the argument the next bullet gives for the human task's: the
  root's membership edge is open for as long as the forest is, so a
  stale warning that counted it could never fire on a live forest at all
  (§forgejo/decisions/commissioning). Where that edge is a delivery's
  only remaining hold, the wait is the forest's closing, and closing the
  root is a person's act — the ⚠️ still says a person is what remains,
  even where the merge gate counts the edge and holds the merge until
  the forest closes (§forgejo/decisions/merge-gate). It warns once the
  shape has held longer than the **stale-merge threshold**, the
  `stale_merge_threshold` config option, one hour where the config says
  nothing (§forgejo/config/validation). The wait is dated from the
  verdict that satisfied the chain — the latest of the standing
  approvals — so an approval a moment old is not stale and a head that
  has since moved is not approved at all. A verdict that carries no
  moment dates nothing, and the walk says nothing rather than borrowing
  another object's timestamp for it; a Forgejo review always carries one.
- **`human_task_blocking`** — an open member carrying `human-task`
  (§forgejo/decisions/transition-table) whose blockers are all closed,
  so nothing stands between the person and the ticket. The forest's own
  rows are membership, not work orders (§forgejo/decisions/commissioning):
  the root's membership edge — open for as long as the forest is — and
  the tracker gate hold no warning back, or no human task could ever
  warn. Neither does the member's own delivery: a pull-request row is a
  delivery and never a work order, the way the merge gate excludes the
  delivering pull request itself
  (§forgejo/decisions/merge-gate), or the ⚠️ would go
  silent for the one ticket whose delivery is up for review — the exact
  state it exists to publish. The wait is dated from the last blocker to
  close, that being the moment the ticket became the person's; a member
  with no work-order blocker at all is dated from its own last activity,
  the newest moment the read carries.

Both kinds are found by walking the members along the root's membership
edges — the one `IssueBlocks` hop out of the root the forest already is,
read the way the digest reads it (§forgejo/decisions/commissioning,
§forgejo/decisions/actionable-state-digest) — on the sweep's tick with
the enumeration, so the request path still costs no forge read. The test
is against the clock, never against a watermark: the question is how long
a human has been unnecessary, not whether the engine has acted
(§forgejo/state/watermark).

A blocking-edge row names whichever repository its author had in mind, and a
body names whichever issues its author had in mind, so the walk admits a
repository the way every other cross-repository read admits one: the scope test
runs before anything is read there (§forgejo/webhook/repository-roles,
§forgejo/webhook/re-fetch). It is paid for both shapes the stale question
reaches — a member's own repository, and every object the delivery's references
or blocking edges name, which the walk screens before it reads any of them. An
object whose repository holds no maitred role is read nowhere: a member's warns
nothing, and one the ask reaches holds the whole warning back — an authored edge
or body must not direct the engine's token into a repository no role names,
once per sweep tick, nor publish what it finds on this unauthenticated port —
and scope the engine cannot establish fails the walk rather than reading on.
The same screen counts the graph against the read budget the gate's resolving
loop charges, distinct objects the way that loop deduplicates them, so a
delivery whose graph reaches past the budget costs no blocker read and asserts
no wait: which rows the hop reads is graph state an author chooses, and where
the merge path pays one read per row the delivery states, the walk — which
pays the same hop on every tick, for every stalled delivery — will not pay past
the count (§forgejo/webhook/re-fetch). The listing the screen reads is the
listing the assembly pays: the screened rows are handed to the gate's own
assembly rather than the hop being read a second time inside it, so one
listing is read per stalled delivery per tick, an edge an author churns
between two listings can never be paid for unscreened, and no second
assembly stands ready to drift. A repository whose pull requests
unit is off is not asked, the way the sweep does not ask it
(§forgejo/reconciliation/the-sweep): it holds no delivery, which is an
ordinary state and not a failed read — and *delivers nothing* is a state
the engine says out loud, since it is also what a repository the snapshot
does not know means (§forgejo/decisions/sync-set). The two predicates ask
different questions and pay for them separately: a human task reads the
member's own blockers, and a stalled delivery costs the connected-issues
read the gate pays, within the budget that bounds the gate's and screened
before it is paid. Both are
paid only for a wait that would otherwise warn, after the head's own
checks. The pull-request listing is shared per repository, the sharing
the landing-set predicate already applies (§forgejo/decisions/sync-set).

What is left to fail is a read the engine should have been able to pay,
and one such failure fails the whole enumeration: the endpoint answers
`500` for every forest until a sweep succeeds, rather than presenting an
unchecked forest as a healthy one. That is the fail-closed rule the view
already applies to its own reads, taken to the wider surface the warnings
touch — the whole view goes rather than one forest going quietly, which
is the trade the rule makes on purpose
(§forgejo/observability/the-outcomes-view). The forge's definitive `404`
excepts itself, and only at the read that first asks whether an object the
walk was pointed at is there — a member's own blockers read, the delivery's
own pull-request read, the delivery's own blocking-edge listing — which says
*that* object has been deleted since the listing that carried it, and a
wait that is gone waits on nobody: its warning is held back with a logged
line naming the reference that answered, the ordinary answer a dangling
reference already gets on the merge path (§forgejo/webhook/re-fetch). The
logged line is there for the reason the pairing's skipped row is
(§forgejo/observability/the-outcomes-view). A `404` from any other read
names some other object — an edge row, a listing, an issue the delivery
mentions — or a read of an object this walk already established, the
delivery's reviews being that case; reading either as the walked object's
absence would hold a real stall back while `warnings_walked: true` published
the forest as checked, so it fails the walk with every other failure.

A warning's title and HTML URL are text their authors wrote, in
repositories the view did not previously name. The page that renders a
forest's warnings inserts them as text and never as markup, so an author
writing a title cannot write into the document
(§forgejo/observability/the-dashboard-view).

## Triggering a sweep

The sweeps are the backstop for deliveries the event path missed, and a
human who has just fixed such a miss wants one to run now rather than at
the next tick. The engine therefore declares its **sweep operations**:
a catalog of named sweeps, each with an `id` and a `title`. The catalog
is the whole set of sweeps the engine offers — today the reconciliation
sweep alone (§forgejo/reconciliation/the-sweep) — and every surface
that lets a human trigger a sweep reads it from the catalog, so adding
a sweep never means touching those surfaces.

The dashboard API serves the catalog and the trigger:

- the forgejo view's response carries the catalog as its **sweeps**
  field — one entry per sweep operation, and an empty list while no
  engine is up (§forgejo/observability/the-dashboard-view),
- `POST /api/forgejo/sweeps/{id}` starts the named sweep immediately
  and answers `202 Accepted`. A sweep runs asynchronously — it can make
  many API calls — so the response reports acceptance, not completion.
  The start belongs to the loop that owns the schedule, so the
  skip-if-in-flight rule and the stop-time wait for the in-flight
  sweep cover a manual start exactly as they cover a scheduled one
  (§forgejo/reconciliation/scheduling). An unknown id answers `404`; an
  engine that is not up — none, or one whose sweep loop is not running
  — answers `503`; a method other than POST answers `405`.

The dashboard renders **one button per sweep operation**, labelled with
the sweep's title. Pressing it opens a **confirmation modal** before
anything runs: the button is an admission that the automatic paths
missed something, so the modal names the sweep, warns that it re-reads
and re-decides everything the engine tracks, and asks the user whether
they have taken the steps that would make the button unnecessary —
working webhook deliveries, a healthy engine — so it is not pressed as
a substitute for fixing what made it needed. Nothing is sent until the
modal is confirmed; cancelling it closes it without a request.

The trigger is a state-changing endpoint on the dashboard port, and it
rides the dashboard's standing trust model: the dashboard serves its
interactive controls unauthenticated, on the assumption that the port
reaches only its operator's network — the assumption the README's Web
Dashboard section spells out. A deployment that cannot keep that
assumption must place its own access control in front of the port.

## Why the engine is off

An engine that did not come up is not one diagnosis but several, and the
surfaces that report it — the disabled route's 503 body, the health check,
and the dashboard's Forgejo view (§forgejo/webhook/the-route,
§overview/scope) — must name the same cause without anyone reading a
startup log. The cause is therefore typed at the place it is produced
(`errors.Is`-able, not matched by text), and it classifies into exactly
one **category**, whose printed name is the same on every surface:

| Category | Means | A fault? |
|---|---|---|
| not configured | nothing is configured at the configured location (§forgejo/config/loading) | no |
| config unreadable | something is there, but it cannot be stat-ed, read, or parsed (§forgejo/config/loading) | yes |
| config or binary mismatch | the config and the binary disagree about the config's key set, in either direction — a missing or unknown action, or a key the binary does not declare (§forgejo/config/validation, §forgejo/config/identity-collapse) | yes |
| credentials missing | the config enables the engine, the environment cannot serve it (§forgejo/webhook/configuration) | yes |
| other fault | any failure the named causes do not describe | yes |

A **location that exists but yields no config at all** — a directory with
no `.yaml`/`.yml` file in it, a file of zero bytes — is a fault, not the
by-design absence of one: the operator pointed the engine at something, and
what it holds is not a config, so the load fails validation and classifies
as *other fault* (§forgejo/config/loading). Only the absence of the
location itself is *not configured*.

*Naming half a landing set* is the failure this distinction exists for: a
config deployed without its binary, or a binary without its config, leaves
the engine inert, and that must be told apart from a typo in a prompt
block. Whether a state is a fault is part of the vocabulary, not a
convention each surface invents: **no configuration at all is not a
fault**, and a surface that reports faults must stay silent about it.

The classification is **runnable without constructing the engine** — it
takes the config path and the environment and returns the cause and its
category, opening no store, building no client, and making no contact with
the Forgejo instance. A health probe has no business causing those side
effects, and the instance may be unreachable while the question is being
asked (§forgejo/webhook/the-route). It walks every construction step that
depends on nothing but the config and the environment — loading the config,
resolving the shared secret, checking the credentials, and loading the mTLS
certificate pair the environment names (§forgejo/client/configuration) — in
the order startup takes them, sharing one function per step with the startup
path so the two cannot disagree about the same deployment.

What a probe therefore cannot see is every failure that only the act of
constructing an object can find: the store directory startup creates, and
anything a constructor fails on beyond the environment-derived inputs above.
Such a failure classifies from the startup error the engine produced
(*other fault*, unless a named cause describes it), and a surface that has
the process's own startup error in hand must prefer that over a probe of its
own.

A surface prints a **category only alongside a cause**. Where the
classification returns no cause the engine is up, and the surface says so in
its own words and names no category: the zero value of the enum prints as
*not configured*, which is a statement about an engine that was never
configured rather than one that is running.

The raw error text keeps going to the log verbatim: the category is how a
surface names the cause in one word, not a replacement for the message
that already carries the path and the action.

The health check (`maitred --health`, §overview/scope) is exactly this
classification run by a probe: it fails on any fault with the category
named in its text, stays healthy on the by-design absence of
configuration — with a note saying the engine is not configured — and
says nothing about an engine that is up.
