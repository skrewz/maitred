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
environment. Otherwise the route is not mounted, and the trigger-based
handler serves `/v1/` as before.

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
   (§forgejo/observability/the-decision-log); nothing is re-fetched,
   dispatched, or cascaded for it, and the delivery is acknowledged. A
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
   dispatch identity: the key's repo, kind, and number, the action, and the
   revision — so a queue system with de-duplication (hotelier's
   `dedup_key`) squelches a re-dispatch of the same `(action, revision)`
   while the first task is still pending. A different revision (a new commit,
   an edited issue) is a different key and dispatches freely. The key carries
   the repo verbatim (slash retained — it is opaque to the queue system), so
   two distinct repos never produce the same key.
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
| work-item source | `maitred-enabled` | `implement`, `reassess`, `review`, `re-review`, `rebase`, `fix-feedback`, `merge-or-wait` |
| outcomes source | `maitred-outcomes-repo` | `decompose`, `wrap-up` |

One organisation listing feeds every repository's roles
(§forgejo/webhook/the-scope-cache, §forgejo/reconciliation/the-sweep); a
repository may hold both roles or neither.

Two distinct rules consult the roles:

- **Admission** — whether a delivery may be considered at all: the
  repository of its object holds at least one role
  (§forgejo/webhook/the-pipeline, step 3). Admission stays a Decision, never
  a listener drop: a delivery from an out-of-remit repository appears in the
  decision log as a hold-off.
- **Admissibility** — whether an action may fire for an object: granted by
  the roles of the repository holding **the object under consideration** —
  the event's object on the event path and the sweep, the cascade key for
  the unblock cascade — never by the roles of the repository the event
  arrived from. A cascade therefore crosses the role boundary: a closed
  issue in an outcomes repository dispatches `implement` for a member in an
  enabled repository. An action denied by this rule is a hold-off naming
  the action, the repository, and the required role's topic; an action no
  role admits (an unknown one) is a hold-off stating
  `action <a> is admitted by no maitred role` — never the wording of a
  role that does not admit it.

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
deciding (§forgejo/client/operations). Entries are therefore never older
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

Everything else — push, create, delete, fork, release, wiki, package,
workflows, `issue_assign`, `issue_milestone`, `pull_request_comment`,
`pull_request_assign`, `pull_request_label`, `pull_request_milestone`,
`pull_request_review_request`, and any `issues` or `pull_request` action
not listed (e.g. `reopened`) — is ignored.

## Re-fetch

For each decision event, the re-fetch assembles the state
§forgejo/decisions/inputs demands:

- **Issue events** — the issue itself. For issue opened (and the sweep's
  synthetic current-state event), the issue's blockers as well; the
  connected open PRs are the pull requests among those blockers. For
  issue closed, the blocker graph rooted at the closed issue.
- **PR events** — the PR itself, and its reviews, for the review
  transitions. For PR merged, the blocker graph rooted at the connected
  issue — the non-PR issue the PR blocks.

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
