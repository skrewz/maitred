# Forgejo observability

The engine is observable: a human can follow why the engine did or did
not act, and can see at a glance the issues and pull requests the
engine tracks.

## The decision log

The engine **logs every decision** — the dispatched action plus its
reason, or the hold-off plus its reason — keyed by repo/issue/PR (the
watermark key, §forgejo/state/storage). The log entry is
**structured**: the key, the event that produced the decision, the
decision (dispatch or hold-off), the dispatched action, the reason, and
the revision the decision was made at are discrete fields, so a human
or a tool can filter the log per object and follow the engine's
reasoning. Where a reason quotes a revision, it names the facts behind it — the
instant a row keyed on `updated_at` carries, or the state, labels and masked-body
digest an `outcome` tracker's revision folds — so a line stays readable against
the forge's history; a revision that describes more than the row in hand, such as
a landing set's, is quoted as it stands (§forgejo/state/watermark,
§forgejo/decisions/sync-set).

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
  dispatched through the main slot. Fan-out dispatches do not touch
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
engine therefore offers an **outcomes view**: the **current forests** of
the organisation — every open `ideation` issue in the repositories holding
the outcomes role (§forgejo/webhook/repository-roles), paired with the
`outcome` issue it blocks. The view introduces no new label vocabulary:
it reads only the reserved `ideation` and `outcome` names
(§forgejo/decisions/actions).

The pairing is found the way the cascade finds connected issues: for each
open ideation issue the engine asks which issues it blocks
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
`outcome` issue that no open ideation root blocks is not a forest and is
not listed. The view is a picture of what exists, not of what an order
is about to do: an `ideation` root already carrying `teardown-forest` or
`sync` — a forest under teardown or mid-sync — is still among the
current forests, whatever the decompose gate makes of the same row
(§forgejo/decisions/forest-teardown, §forgejo/decisions/sync-set).

The dashboard API serves the view at `GET /api/forgejo/outcomes`. The
set it serves is the one the last sweep tick recorded: like the tracked
view it answers from memory, and a request costs no forge read
(§forgejo/observability/the-dashboard-view). The response is an object
carrying two things: the **forest list** and **one engine-status field**
rendering the engine's state as the forgejo view does
(§forgejo/observability/the-dashboard-view), so a configured-but-broken
engine never reads as a healthy one with nothing to show. Each forest
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
always present: **`chart`**, the Mermaid source of the tracker's
current-state block, and **`chart_revisited`**, that region's recency
stamp, both scraped as described below — and its **warnings**, the ⚠️
marks saying whether the forest is waiting on a person, an empty list
when it is not
(§forgejo/observability/the-warnings-a-forest-carries). Without the
engine, or
with no forests, the list is empty — never `null`: an enumeration that
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

**Scraping the current-state block.** The picture is not composed by the
engine: it is read out of the tracker's description, which is where the
forest's picture already lives. That description is the blocks row the
walk already holds — the listing and the single-issue read answer with
the same model and fill `body` the same way, so scraping the row costs
no second read of an issue the enumeration is already holding
(§forgejo/client/operations). The region scraped is the one the
engine delimits with the HTML-comment fence pair
`<!-- maitred:forest:begin -->` and `<!-- maitred:forest:end -->` — the
view reads that pair and writes nothing: this path makes no mutation, so
the tracker's description is left byte-for-byte as found
(§forgejo/client/operations, §forgejo/client/the-one-mutation). The
region is the engine's own drawing, so the scrape pays the admission test
the write pays (§forgejo/reconciliation/the-tracker-block). Within the
region the view carries two things and nothing else: the fenced
`mermaid` block between its fences, **verbatim** — never re-indented,
repaired, or completed in code — and the
`Last revisited: <YYYY-MM-DD HH:MM> <TZ>` line, whose value is carried
**as written**, so a UI states how recent
the picture is without parsing prose and an abbreviation of `CET` reads
exactly as the agent wrote it. Neither field is parsed further: the
stamp is a string the view does not interpret.

Verbatim is a promise about the text, not about its source. Whatever
stands between the fences reaches the payload untouched, and a
description is editable by anyone with write access to the tracker's
repository — the fence pair is a scraping contract, never an access
control — mirrored onto a port that authenticates nothing
(§forgejo/observability/why-the-engine-is-off). The rendered source a
writer splices in carries no `click`, no `subgraph` and no comment
(§forgejo/renderer/the-source), but the view cannot tell rendered source
from hand-written text, and Mermaid drawn at a scripting security level
runs `click` and `link` callbacks in the viewer. So anything drawing
`chart` treats it as untrusted input: render it at a non-scripting
security level, or show it as text and never as a graph.

What scraping promises is exactly one well-formed region, and every
ragged edge publishes **nothing** rather than a guess. The refusal is
per field, not per region: the stamp describes the **region**, so a
region holding two pictures or none still states when it was last
revisited, and one holding two stamps still states its picture — "last
revisited at X, and it holds no single picture" is a thing a tracker can
honestly be, which is why a UI printing recency above a graph must read
the fields as two statements rather than one. The edges: no fence pair,
two or more fence pairs (which region belongs to whom is not guessable),
a region holding no fenced Mermaid block or more than one, a Mermaid
fence never closed, and a region carrying no stamp line or more than one.
A `Last revisited:` line carrying the label and no value counts as one of
those lines rather than as an absence, so a placeholder above the stamp
the agent later wrote publishes nothing. The two ways a region says nothing
about recency are told apart by whether the line is there at all: the
template the curating agent writes carries the label, so a line with nothing
after it is a recency not yet written, while a region holding no such line
was written by something other than that template — the contract drifting,
which the log says and silence would hide. An empty field is a statement
about the tracker's description, never about a read that broke: the
description came from a read that succeeded, since a blocks read that
fails fails the enumeration the way every other failed read does,
answering `500` rather than publishing the empty chart a stranger would
read as a forest with no picture. A forest whose tracker is absent
publishes both fields empty.

What the log says about an empty field is therefore the one thing a
reader cannot see in the payload, and the two families that produce one
are said apart. A description carrying **neither** marker is a tracker
whose picture has not been written yet — an ordinary state rather than a
fault, what a young organisation looks like from the outside — and it
leaves no trace at any level, because repeating the same sentence for it
on every tick would bury the failures the log is for: a read that broke,
which fails the enumeration and reaches the log loudly. A lone `Last
revisited:` line carrying the label and no value belongs to that family
seen from the field side: the stamp has not been written yet, so the stamp
publishes nothing and nothing is said — the picture the same region holds
still stands in the payload. A region that was
there and was refused is the other family, the one a fence pair drifting
away from what the curating agent writes lands in, and it says so once
per tick at `Debug`: the tracker, and the rule that refused. Debug rather
than above it because the refusal is not a fault either, only a
statement that the contract and the description have stopped agreeing —
information for whoever is looking, noise for whoever is not. Without
that line the two
families are indistinguishable from outside, and the drift the marker
contract depends on would show up as an empty picture rather than as a
rule named.

The enumeration happens on the reconciliation sweep's tick, not on the
request path (§forgejo/reconciliation/the-sweep): per tick it costs one
issue listing per outcomes repository and one blocks read per open
ideation root, on top of the sweep's own walk; the picture is scraped
from the rows those reads return, so it adds no read of its own. The
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
route costs the server nothing new. The page polls the endpoint on a
timer and renders **one row per current forest**: the repository the
forest grew in, the ideation root as a link carrying its state, and
the tracker as a link carrying its state — or the absence of one,
stated in the row rather than dropped. The rows are tall so the forest
chart has room underneath when that work lands. The link is only a
link when the forge's URL is one a link can carry (http or https);
anything else renders as text, since the port authenticates nothing.
A forest the payload says is waiting on a person draws its **warnings**
in the row: one ⚠️ entry per warning, each naming its kind, the issue
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
whose last sweep found nothing says there are no current forests.
A view the engine answers `500` for says it could not be read, so an
empty rectangle never passes for a dashboard
(§forgejo/observability/the-dashboard-view). The header links to the
route so it is reachable by eye; it carries one link, which reads as
the way back when the route is where the reader already is. The
header's trigger statistics — numbers this page never reads — stay
hidden on the route, so they state nothing rather than rendering
their defaults as facts.
### The warnings a forest carries

A forest waits on a person in exactly two ways, and each forest of the
payload carries a **warnings** list saying which — and nothing else.
Each warning is a reference (repository, number, title and HTML URL, the
same four fields a forest row carries and for the same reason, since what
holds a forest up may live across the organisation), its **kind**, and
the **age** of the condition in seconds, so the dashboard links to the
object rather than constructing the judgement from reads of its own. A
forest nothing is waiting on carries an empty list, never `null` — the
honest empty the forest list itself is. A warning reports; it dispatches
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
(§forgejo/observability/the-outcomes-view).

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
