# Forgejo webhook

The engine serves the organisation's single "all events" webhook. The
event is a **notification**; Forgejo is the **source of truth**. On every
delivery the engine re-fetches the authoritative state of the affected
object before deciding, which is what makes the engine robust to
out-of-order, missed, and concurrent deliveries.

## The route

The handler is mounted at `POST /v1/forgejo/all_events` on the webhook API
port, alongside the trigger-based webhook handler and **taking precedence
over** its `/v1/{provider}/{endpoint}` route for that path. The org
webhook is configured once, in Forgejo, pointing at this route; there are
no per-trigger webhooks for the engine.

The engine is enabled when the engine config loads
(§forgejo/config/loading) and the Forgejo credentials are present in the
environment.

**While the engine is disabled** the route is still mounted — mounted
whenever the webhook API server runs — and every delivery to it is
**refused**:

- The response is `503 Service Unavailable`, the body naming the **category**
  of the startup error (§forgejo/observability/why-the-engine-is-off) — one
  word, never the raw error text. The body's reader is Forgejo, which logs
  it and displays it in a delivery list people read, so a config error
  quoting a filesystem path does not belong in it.
- One line is logged per refused delivery, naming the request path, the
  delivery id (`X-Forgejo-Delivery`), the same category, and the full startup
  error as `reason`: the detail is relocated to the log, not lost. Per-delivery
  silence is a defect: a refused delivery must be findable from Forgejo's
  delivery log alone.
- The body is never read and nothing is verified, mapped, or dispatched.

`503`, not `404`, is the diagnosis. Forgejo keeps the most recent
deliveries and offers manual redelivery, but does not retry
automatically; the status code is therefore read by a human, not
consumed by a retry loop. `404 unknown provider` — what the trigger
handler's `/v1/` fallback makes of the path — reads "nobody ever
registered this endpoint" and points at the webhook configuration.
`503` reads "the endpoint is here and cannot serve yet" and points at
the engine's startup log, where the answer already is. The trigger
fallback itself is unchanged: an unconfigured trigger provider's own
route still answers `404`.

## The pipeline

On each `POST`:

1. **Verify the signature.** The `X-Forgejo-Signature` header must carry
   the hex-encoded HMAC-SHA256 of the raw body computed with the shared
   secret. A missing or mismatched signature is rejected with `401` and
   nothing is processed.
2. **Filter the event.** The event is identified by the
   `X-Forgejo-Event-Type` header and the payload's `action` field.
   Deliveries that are not an issue, pull request, or review transition
   (the table below) are **cheaply ignored** — no re-fetch, no decision —
   and acknowledged.
3. **Test the scope.** A repository holds **roles** by topic
   (§forgejo/webhook/repository-roles). A delivery may be considered when
   the repository of its object holds **at least one** role; the roles
   scope **both** engine paths, not just the sweep. The test is made from
   the event payload's repository, **before** any re-fetch. A repository
   that holds no role — or whose topics the engine does not know —
   **holds off** with the reason
   `repo <name> holds no maitred role (neither maitred-enabled nor
   maitred-outcomes-repo)`, logged like any other hold-off
   (§forgejo/observability/the-decision-log); nothing is re-fetched or
   dispatched for its object, and the delivery is acknowledged. A
   **closed issue** in such a repository is the one exception to the
   read: nothing is ever acted on for the closed issue itself, but the
   engine asks what it blocks and runs the unblock cascade for whatever
   it blocks in a role-holding repository
   (§forgejo/webhook/repository-roles, §forgejo/decisions/unblock-cascade)
   — a role-less repository stays inert, never opaque. The hold-off
   reason distinguishes the two kinds of inert close: one that blocks
   nothing tracked names the remit alone; one that sits in the blocker
   graph additionally names the unblock cascade the close woke. A
   repository listing that **fails** also holds off, but with a reason
   naming the failure — never the role or topic wording — so a transient
   API failure is not recorded as a deliberate exclusion
   (§forgejo/webhook/the-scope-cache). A delivery that passes the scope
   test may still be denied by **admissibility** further down the
   pipeline: an action fires for an object only if the repository holding
   the object under consideration carries the role that admits it
   (§forgejo/webhook/repository-roles).
4. **Re-fetch the state.** The affected issue/PR — plus what the
   transition needs (reviews, blockers, connected PRs, blocker graph) — is
   re-fetched from Forgejo through the read-only client
   (§forgejo/client/operations). The event's own claims are never trusted.
   A `pull_request` delivery with action `closed` is refined by the
   re-fetch: a merged PR is a PR-merged event, an unmerged one a
   PR-closed event.
5. **Decide.** `Decide(event, state, watermark)`
   (§forgejo/decisions/the-decision-function) — the re-fetched state wins
   over the event.
6. **Dispatch.** A dispatch fills the action's canned prompt
   (§forgejo/config/placeholders) and enqueues the task through the queue
   provider, with the persona and timeout from the config
   (§forgejo/config/canned-prompts). The task carries a **dedup key** — the
   dispatch identity: the key's repo, kind, and number, the action, the
   **subject** for a fan-out dispatch (the lens identity
   §forgejo/decisions/review-lenses), and the revision — so a queue system
   with de-duplication (hotelier's `dedup_key`) squelches a re-dispatch of
   the same `(action, subject, revision)` while the first task is still
   pending. The subject is what keeps one lens's review task from squelching
   another's at the same revision; singletons carry no subject slot. A
   different revision (a new commit, an edited issue) is a different key and
   dispatches freely. The key carries
   the repo verbatim (slash retained — it is opaque to the queue system), so
   two distinct repos never produce the same key. A prompt that references a
   URL the re-fetched state cannot fill fails the dispatch instead of
   enqueueing an invocation that names no object
   (§forgejo/config/canned-prompts); the failure is logged and nothing is
   watermarked, so a later sweep or event tries again. A review fan-out
   (the decision naming several lenses, §forgejo/decisions/review-lenses)
   is one dispatch per lens in config order, each filling that lens's
   canned prompt under that lens's persona and timeout, and each writing
   its own subject slot in the watermark.
   The task also **names its work**: its `title` is composed by one helper
   for both event kinds — the action's verb, the title of the object the
   state carries (the pull request where one is present, the issue
   otherwise), and the `repo#number` reference, e.g.
   `Review: Make everything awesome and pink (o/r#1337)` — with the
   subject capped so a long issue title cannot produce an unreadable row.
   Its `url` is the pull request's `html_url` where the state carries one,
   the issue's otherwise, and empty where the state carries neither — the
   engine never invents a link. A state with no object at all therefore
   yields a title naming only the action and the reference, and no URL.
   Its `priority` names the tier the queue should serve the task at, and is
   the `vip` label's doing alone: unset unless a carrier wears that label,
   per §forgejo/webhook/vip-priority.
7. **Watermark.** A dispatch records its watermark — the action and the
   revision (the PR head sha, or the issue's `updated_at`) — in the store
   (§forgejo/state/watermark); a hold-off records nothing. Each key of the
   unblock cascade (§forgejo/decisions/unblock-cascade) has its own
   watermark applied before its dispatch, so an already-dispatched
   `(action, revision)` is not re-dispatched.
8. **Acknowledge.** The response is `204` once the pipeline has run; the
   engine does not block on the dispatched agent — dispatch only enqueues.
   A slow pipeline (several re-fetch calls) can exceed Forgejo's delivery
   timeout, in which case Forgejo records the delivery as failed without
   retrying; the event is still processed to completion, and the
   reconciliation sweep re-derives anything genuinely missed.

A re-fetch failure is a hold-off: it is logged and acknowledged, and the
reconciliation sweep re-derives from the source of truth.

### Repository roles

The engine's remit is a set of repository **roles**, held by topic, not
one repository set. Topics are **reserved vocabulary** — no repository
name appears in the engine or its config:

| role | topic | grants admissibility to |
|---|---|---|
| work-item source | `maitred-enabled` | `implement`, `reassess`, `review` (the lens fan-out, §forgejo/decisions/review-lenses), `rebase`, `fix-feedback`, `merge-or-wait` |
| outcomes source | `maitred-outcomes-repo` | `decompose`, `wrap-up`, `teardown-forest`, `sync` |

One organisation listing feeds every repository's roles
(§forgejo/webhook/the-scope-cache, §forgejo/reconciliation/the-sweep); a
repository may hold both roles or neither.

Two distinct rules consult the roles. Roles decide which repositories
the engine **acts on**; they were never meant to decide which
repositories the graph may be **read through**:

- **Admission** — whether a delivery may be considered at all: the
  repository of its object holds at least one role
  (§forgejo/webhook/the-pipeline, step 3). Admission stays a Decision, never
  a listener drop: a delivery from an out-of-remit repository appears in the
  decision log as a hold-off. Admission governs what the engine acts on,
  not what the graph may be read through: an issue **closed** in a
  role-less repository wakes the unblock cascade for the issues it
  blocks, each emission's admissibility still judged by the roles of
  the repository holding its key — never by the roles of the
  repository the event arrived from (§forgejo/decisions/unblock-cascade).
  This extra read is restricted to **closes**: opens, edits, labels and
  comments in an inert repository keep the zero-cost pre-re-fetch
  hold-off, and no other decision — no `implement`, no `reassess`, no
  review, no merge — may ever follow from an event in a role-less
  repository.
- **Admissibility** — whether an action may fire for an object: granted by
  the roles of the repository holding **the object under consideration** —
  the event's object on the event path and the sweep, the cascade key for
  the unblock cascade — never by the roles of the repository the event
  arrived from. A cascade therefore crosses the role boundary in both
  directions: a closed issue in an outcomes repository dispatches
  `implement` for a member in an enabled repository, and a cascade
  rooted at an issue in a work-item repository dispatches `wrap-up`
  for a newly unblocked tracker in an outcomes repository
  (§forgejo/decisions/unblock-cascade). An action denied by this rule
  is a hold-off naming the action, the repository, and the required
  role's topic; an action no
  role admits (an unknown one) is a hold-off stating
  `action <a> is admitted by no maitred role` — never the wording of a
  role that does not admit it. A cascade **hold-off** that names an
  action is qualified by the same test — the roles of the repository
  holding its key, which the scope cache makes free — and where they
  deny it the record says so in the same wording, so the decision log
  never promises an action those roles exclude.

Admissibility is applied **after** the re-fetch and `Decide`, not beside
admission at step 3, and this shape is deliberate. Admissibility judges
an **action** against the roles of the repository holding **the object
under consideration**; the action is the decision function's answer, not
the event's, and the object's repository only differs from the event's
for cascade keys, which step 3 never sees. Testing an event's whole
action family before the re-fetch would hard-code an event-to-action-family
mapping the pipeline otherwise does not carry, one the outcomes chain
(#83, #84, #85) will redraw. The accepted cost: a delivery for an
outcomes-only repository (or a reconciled object in one) is re-fetched
before its work-item action is denied — one re-fetch per object, paid
for the visibility that each denial is a recorded, self-healing
hold-off in the decision log rather than a silent no-op
(§forgejo/reconciliation/the-sweep).

Unknown scope keeps these semantics verbatim: fail closed, hold off naming
the listing failure — never "lacks `maitred-outcomes-repo`"
(§forgejo/webhook/the-scope-cache).

### The scope cache

The scope test does not add an API round-trip to every delivery. The
engine keeps an in-memory **scope cache**: a snapshot of the
organisation's repositories and which roles each holds — derived from
**one** repository listing per refresh, so both roles come from the same
snapshot. The reconciliation sweep refreshes it from the
listing it already enumerates (§forgejo/reconciliation/the-sweep), and an
event path that finds the cache empty or older than one
`reconcile_interval` refreshes it with a single repository listing before
deciding (§forgejo/client/operations). The snapshot is looked up by
name **case-insensitively** — the way the forge addresses a repository
and the way the closing-reference grammar matches one — so a
reference's spelling and a listing's full name agree on the roles.
Entries are therefore never older
than one interval: a repository whose topic has just been removed is not
acted on for longer than that. A repository absent from the snapshot holds
no role — the test **fails closed**. Deliveries that find the cache
stale **share one refresh**: the refresh is single-flight, so a burst
against a stale cache costs one repository listing, not one each.
A repository listing that **fails** is also a hold-off — logged,
acknowledged, and re-derived by the next sweep, which refreshes the
cache again (§forgejo/reconciliation/failure-handling) — with a reason
naming the listing failure, distinct from the `holds no maitred role`
wording, so an API failure never reads as an exclusion. The failed
attempt is stamped like a successful one: while the listing keeps
failing, re-attempts are bounded to a short retry interval rather than
one listing per delivery. The last known snapshot is kept across the
failure but **never acted on while stale** — scope stays unknown, and
the test still fails closed. The keys of an unblock cascade are tested the
same way before their dispatch, against the role admitting the cascaded
action (§forgejo/webhook/repository-roles), so a cascade never starts an
agent in a repository where that action is inadmissible; a cascade
hold-off is recorded in the decision log like any other decision,
wherever it is made (§forgejo/observability/the-decision-log).

### VIP priority

The `vip` label names the tier a task is scheduled at. It is an
**annotation, not a trigger**: it asks for no work item and gates no
decision, and attaching or removing it is just editing labels
(§forgejo/decisions/actions). The priority itself is the queue's own
vocabulary, forwarded verbatim and never interpreted by maitred
(§overview/scope); the tier this section names is `teacher`, and
where nothing carries the label the field stays absent from the wire body
and the queue's default tier applies — the body is the one maitred
produced before the field existed.

The label is judged **at the moment of scheduling** and read fresh on
every dispatch: it is never cached in engine state between schedulings,
so attaching `vip` lifts the next dispatch and removing it lowers that
dispatch, with no retroactive change to a task already queued. The
decision is made in the one dispatch pipeline
(§forgejo/webhook/the-pipeline), so the event path, the sweep, the review
fan-out, the landing set's row and the unblock cascade cannot drift apart.

A dispatch is scheduled at `teacher` when one of these holds:

| Carrier | Read |
|---|---|
| the pull request being scheduled | its own re-fetched labels |
| an issue the pull request delivers | the connected-issue rows the merge gate already reads (§forgejo/decisions/merge-gate) |
| the tracker of the forest an issue belongs to | the issue's blocker rows: an `outcome` issue wearing the label, gated in turn by the `ideation` root that makes it the forest's tracker (§forgejo/decisions/forest-teardown) |

A tracker wearing the label lifts its **own** dispatches too — its
decompose and wrap-up rows — through the tracker's own label, no walk
needed: the root gating it is the root its forest shares. An `outcome`
issue that wears the label without a root gating it names no forest and
lifts nothing: membership is read, not inferred from a label.

A tracker's label travels as far as its forest: it lifts every dispatch
of every member the tracker gates, in every repository that forest
spans, while an object's own label lifts that object alone. The forest,
not the repository, is the unit of the carrier's reach.

**Who may wear the label.** Labels in this system are not human-only —
the decomposer writes `forest-seeding` inside its own task
(§forgejo/decisions/transition-table) — and the identities the queue schedules
can edit labels on the objects they work. An annotation that moves work
between queue tiers is therefore not neutral in the way a kind label is,
so `vip` is an org-owned label whose editing belongs to the
organisation's humans: **a task must never attach `vip` to the object it
was scheduled to work on**, and the tier this section promises rests on
that convention as much as on the walk.

The walk pays **no read the dispatch did not already make** when no
carrier wears the label: an object wearing the label costs no hop, and a
forest nobody has labelled vip costs none either, because the label is
what opens the graph. Where a labelled tracker stands in the rows the
walk pays one hop — the tracker's gate — per dispatch, and the review
fan-out pays it once per lens (§forgejo/decisions/review-lenses). The
unblock cascade's issue leg (§forgejo/decisions/unblock-cascade) is the
one path that dispatches an issue without holding its blocker rows, so
it reads them once per dispatch; the cascade's PR leg dispatches the
state the re-fetch assembled, so the delivered issue is judged there on
the same rows every other delivery path judges it on.

A graph read that **fails** costs the tier and not the dispatch: an
object whose forest cannot be read is scheduled at the queue's default
like any unlabelled object, with one warn line naming the lost read —
the level every other degraded continuation in the engine logs at.
Holding the work back over an annotation would leave the queue poorer and
the issue unattended, which is the opposite of what the label asks for.

The tier is an **ask**, made in the queue's own words: the one value the
engine ever puts in the field is a word the queue documents
(§overview/scope — the queue owns that vocabulary), so a refused tier is not
a state the label can create and the engine writes no fallback for one. If
the queue's vocabulary ever changes, the engine's word changes with it —
that is a queue-side change with its own consequences, not a condition a
dispatch path papers over. An enqueue that fails, for the field or for
anything else — a queue that is down, a persona that does not exist — is
the ordinary failure every path already handles: the watermark records
nothing, the sweep retries, and the queue's own explanation rides in the
error rather than being guessed at.

## Event mapping

| `X-Forgejo-Event-Type` | `action` | Decision event |
|---|---|---|
| `issues` | `opened` | issue opened |
| `issues` | `edited` | issue edited |
| `issues` | `closed` | issue closed |
| `issue_label` | `label_updated`, `label_cleared` | issue labels changed |
| `issue_comment` | `created` | issue commented |
| `pull_request` | `opened` | PR opened |
| `pull_request` | `closed` | PR merged or PR closed (refined by the re-fetch) |
| `pull_request_sync` | `synchronized` | PR synced |
| `pull_request_review_approved` | — | PR reviewed |
| `pull_request_review_rejected` | — | PR reviewed |
| `pull_request_review_comment` | — | PR reviewed |
| `pull_request_comment` | `created` | PR commented |

Everything else — push, create, delete, fork, release, wiki, package,
workflows, `issue_assign`, `issue_milestone`, `pull_request_assign`,
`pull_request_label`, `pull_request_milestone`, `pull_request_review_request`,
and any `issues` or `pull_request` action not listed (e.g. `reopened`) — is
ignored.

## Re-fetch

For each decision event, the re-fetch assembles the state
§forgejo/decisions/inputs demands:

- **Issue events** — the issue itself. For issue opened, edited, labels
  changed, and commented (and the sweep's synthetic current-state
  event), the issue's blockers as well; the connected open PRs are the
  pull requests among those blockers. The activity path needs the
  blockers because an open blocker holds off the `reassess` decision
  (§forgejo/decisions/transition-table); if the blockers read fails,
  the re-fetch fails — a hold-off naming the failure, never
  "no blockers".

  An issue carrying both the `ideation` and the `teardown-forest` label
  has its **downstream** edges read too — the issues it blocks — because
  the teardown order is actionable only while a member it would delete
  still stands, and the engine counts rather than asking an agent to
  discover it (§forgejo/decisions/forest-teardown). The count is a label
  test — an `outcome` issue the hop names is a tracker the order spares and
  therefore counts for nothing — so this hop must return each blocked issue
  **with its labels**: a projection that carried only numbers would read a
  spared tracker as a deletable member and re-fire the order on every sweep
  (§forgejo/decisions/forest-teardown). That hop belongs to the teardown
  rows alone: a root under no order performs no `IssueBlocks` call, so the
  curation path costs what it always did. A failure of the hop fails the
  re-fetch, as the blockers' does.

  On the sweep's synthetic current-state event, an issue carrying the
  reserved `sync` label grows one hop further: each blocking issue — a
  member of the landing set — is fetched with its delivering PR and
  that PR's reviews, the state the sync predicate reads
  (§forgejo/decisions/sync-set). The delivering PR is found from the
  pull-request side: an open pull request of the member's own
  repository whose body auto-closes the member — no blocking edge
  between the member and its delivery is read or required, and the
  member's dependency rows are not consulted for one
  (§forgejo/decisions/sync-set, s-git/maitred#171). Where a member
  stands with several auto-closing open pull requests the first in the
  repository's listing is read; the one-delivery-per-member contract
  is the decomposer's to keep (§forgejo/decisions/sync-set). A
  blocking issue that is itself a pull request has no delivering PR to
  hop to: its own state and reviews are the read, the PR being its own
  delivery (§forgejo/decisions/sync-set).
  For issue closed, the blocker graph rooted at the closed issue.
- **PR events** — the PR itself, and its reviews, for the review
  transitions and for PR comments (which decide as reconcile). For PR
  reviewed, PR commented, and the sweep's synthetic
  current-state event for a PR, the connected issues — the issues the
  PR's **body auto-closes** and the non-PR issues it **blocks**, read
  through the forge's closing-keyword grammar and the issue-blocks
  surface respectively, each carrying its own repository, which may
  differ from the PR's — and each issue's
  blockers as well: one hop per connected issue, no recursion, the input
  to the merge gate (§forgejo/decisions/merge-gate,
  §forgejo/decisions/inputs), consulted wherever a merge may be
  dispatched — the PR
  reviewed event and the sweep's PR path (which PR commented also
  decides as).

  Two relations connect an issue to a delivery, and **both are read**.
  The body's auto-close references are the relation the authoring
  contract writes: a landing-set member delivers through the pull
  request whose body closes it, and no blocking edge between the member
  and its delivery is recorded (§forgejo/decisions/sync-set,
  s-git/maitred#171). The blocking edges still carry the **direct**
  shape below, where a pull request stands in a grouping issue's own
  blockers. Reading one relation alone leaves a path blind to what the
  other sees, and the predicate and the gate must decide on the same
  membership: an approval path that could see only the edges found a
  member's delivery connected to nothing, merged it through
  `merge-or-wait`, and left the set to the next sweep
  (s-git/maitred#180). A bare `#N` resolves in the PR's own repository,
  an `owner/repo#N` in the repository it names, exactly as the forge
  resolves it
  (https://forgejo.org/docs/latest/user/linked-references/); because
  Go's regexp engine has no lookahead, the scan rewinds over the
  terminator a match consumes, as the forge's scanner does, so two
  references separated by nothing but a newline both resolve
  (s-git/maitred#185). A reference whose repository names a path
  segment — `.` or `..`, which the grammar's character class admits —
  is carried by the grammar like any other and **refused at
  resolution**, before any URL is built and with a debug line naming
  it: no body-derived string reaches the API URL unvalidated
  (s-git/maitred#185 review 1304). A reference naming a pull request connects
  nothing, a pull request being a delivery and never a connected issue,
  and a reference whose issue answers **not found** — a deleted issue, a
  mistyped number, an illustrative one in prose — is skipped the same
  way: the forge itself silently ignores a reference whose target does
  not exist, and a definitive "no such issue" is a read that succeeded
  in learning there is nothing there, not a failed read to fail closed
  on. The same answer arrives from a **repository the token cannot
  read** — Forgejo guards private repositories with a 404 — so a
  reference into such a repository reads as connecting nothing. That is
  a defensible reading for a read-only client, but it is the case where
  a real relation goes unseen, so every drop — a skip, a refusal, a
  truncation — leaves a debug line naming the resolved repository and
  number — identifiers only, never the body. A cross-repository
  reference additionally pays
  the **remit** test every other cross-repository read pays
  (§forgejo/webhook/repository-roles): the engine's token must hold a
  role in the named repository, or the reference connects nothing and a
  stranger's prose directs no privileged read; an unknown scope fails
  the re-fetch rather than reading on. The repository name a reference
  resolved to carries one canonical identity — lowercased — matching
  the grammar's case-insensitive name test, the dedup key and the
  scope cache's case-insensitive lookup, so a mixed-case spelling of a
  repository inside the remit is neither read twice nor logged as
  outside it (s-git/maitred#185 review 1304). The reads the approval
  path makes are **capped** (eight), capped in the
  resolving loop where the per-reference cost is paid and not in the
  grammar: the authoring contract needs one or two, and the cap bounds
  the fan-out against a body written to cost the engine the delivery;
  the predicate's read of the same body is pure in-memory matching
  that costs nothing per reference and stays uncapped, a faithful
  mirror of the forge. The budget is charged beside the call it bounds,
  so a reference that costs no read — deduped against a blocking-edge
  row, refused as a path segment, turned away by the remit test —
  spends none of it, and a body of repeats followed by genuinely-new
  references resolves what it names rather than reading as
  disconnected (s-git/maitred#185 review 1338). The cap's **overflow
  fails the re-fetch**: a body that asks for more paid reads than the
  cap pays cannot have its relations read completely, and a partial
  answer presented as a complete one would let the survivor set be
  chosen by the body being read — the deliberate fail-open form #180
  exists to close. The first reference the loop can no longer pay for
  leaves a debug line and the re-fetch errors with a hold-off naming
  the failure: the failure mode is a hold-off, never a merge
  (s-git/maitred#185 review 1365). An issue reachable both ways is
  read once, the blocking-edge rows first, so it is counted once.
  Every entry is resolved: the gate's decision is
  never left to the order the listing happens to return; a failed read
  fails the re-fetch — a hold-off naming the failure, never "no
  blockers". The hop is performed wherever a merge may be dispatched,
  and nowhere else: the PR opened and PR synced events grow no such
  read — neither path dispatches a merge, so neither needs the
  gate's rows. An opened pull request therefore costs one graph walk
  fewer than it did, and the sweep's PR path performs exactly the one
  hop the merge gate needs — no walk that served only the retired
  action (§forgejo/decisions/commissioning, s-git/maitred#151). The body
  the references are read from is the PR's own, already fetched, so a
  delivery naming no issue costs no read beyond the hop it always did,
  and the reads an added reference costs are one issue and one blockers
  row — never a listing. A pull
  request opened against a connected issue blocked by an open
  `ideation` issue is decided on its own state — `review` or `rebase` —
  and no graph walk runs for it at all (§forgejo/decisions/transition-table).
  A connected issue carrying `agentic-sync-merge` is a landing-set
  member and grows one outbound
  hop — the open sync issues it blocks, its membership edges, so the
  approval path can emit the set without waiting for the sweep
  (§forgejo/decisions/sync-set). A connected issue carrying `sync`
  whose own blockers include the PR under consideration is the
  **direct** membership: the PR is that set's member and its own delivery, so the
  assembly records the grouping issue as the PR's membership from the
  dependency rows it already read — no hop, no extra call — and the
  hold-off fires on it as on any other, so the predicate and the
  approval path read one shape the same way
  (§forgejo/decisions/sync-set). The unblock cascade's PR leg, the
  third path that can dispatch `merge-or-wait`, assembles this same PR
  state — connected issues and membership edges included — rather than
  a reduced one, so the hold-off sees membership wherever a merge may
  fire (§forgejo/decisions/unblock-cascade). For PR merged, the blocker graph
  rooted at the connected issue — the non-PR issue the PR blocks.

The blocker graph is re-fetched to the depth the unblock cascade walks:
each closed block is followed, each open block is fetched with its
blockers, and a visited set terminates cycles.

## Serialisation

The read-decide-dispatch-update sequence for one key is serialised on the
watermark store's per-key lock (§forgejo/state/concurrency), so
concurrent events for the same issue/PR cannot race the watermark.
Different keys proceed in parallel.

## Configuration

The Forgejo base URL and token, and the watermark directory come from the
environment (`MAITRED_FORGEJO_URL`, `MAITRED_FORGEJO_TOKEN`; the watermark
directory is the data directory's `forgejoeng` sub-directory, per
§forgejo/state/storage). The canned prompts come from the engine config
(§forgejo/config/loading).

The shared secret is read from a file whose path is given by
`MAITRED_FORGEJOENG_SECRET_FILE`; the file's contents, with a single
trailing newline stripped, are the secret. As a transitional measure the
secret may instead be supplied directly in the environment
(`MAITRED_FORGEJOENG_SECRET`); when both are set the file takes
precedence. Carrying the secret in the environment is a smell — it is
retained only so deployments can adopt the file incrementally — and is to
be removed once every deployment reads the secret from a file.

An engine the config has enabled that the environment cannot serve — no
instance URL, no token, no shared secret, or an mTLS pair set only halfway
through (§forgejo/client/configuration) — is a named cause, **the
environment is incomplete**, and classifies as *credentials missing*
(§forgejo/observability/why-the-engine-is-off). It is a fault, not a
deliberate disablement: the config asked for the engine. A secret file
that exists but cannot be read, or that is empty, is a different failure
again — not a missing credential — and is reported with its own message.
