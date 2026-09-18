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
3. **Re-fetch the state.** The affected issue/PR — plus what the
   transition needs (reviews, blockers, connected PRs, blocker graph) — is
   re-fetched from Forgejo through the read-only client
   (§forgejo/client/operations). The event's own claims are never trusted.
   A `pull_request` delivery with action `closed` is refined by the
   re-fetch: a merged PR is a PR-merged event, an unmerged one a
   PR-closed event.
4. **Decide.** `Decide(event, state, watermark)`
   (§forgejo/decisions/the-decision-function) — the re-fetched state wins
   over the event.
5. **Dispatch.** A dispatch fills the action's canned prompt
   (§forgejo/config/placeholders) and enqueues the task through the queue
   provider, with the persona and timeout from the config
   (§forgejo/config/canned-prompts).
6. **Watermark.** A dispatch records its watermark — the action and the
   revision (the PR head sha, or the issue's `updated_at`) — in the store
   (§forgejo/state/watermark); a hold-off records nothing. Each key of the
   unblock cascade (§forgejo/decisions/unblock-cascade) has its own
   watermark applied before its dispatch, so an already-dispatched
   `(action, revision)` is not re-dispatched.
7. **Acknowledge.** The response is `204` — quickly; the engine does not
   block on the dispatched agent.

A re-fetch failure is a hold-off: it is logged and acknowledged, and the
reconciliation sweep re-derives from the source of truth.

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

The shared secret, the Forgejo base URL and token, and the watermark
directory come from the environment (`MAITRED_FORGEJOENG_SECRET`,
`MAITRED_FORGEJO_URL`, `MAITRED_FORGEJO_TOKEN`; the watermark directory is
the data directory's `forgejoeng` sub-directory, per
§forgejo/state/storage). The canned prompts come from the engine config
(§forgejo/config/loading).
