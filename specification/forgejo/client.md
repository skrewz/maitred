# Forgejo client

The engine talks to Forgejo through a client that exposes exactly the
operations the decision function and the event/reconciliation paths need. It
carries **one mutation** — the narrow issue-body write that lands a tracker's
current-state block (§forgejo/reconciliation/the-tracker-block) — and no
other: every other side effect the engine has is a dispatched task
(§overview/non-goals).

## Operations

The client exposes these read operations over the Forgejo REST API v1,
authenticated with a bearer token:

| Operation | What the engine does with it |
|---|---|
| list an org's repositories (with `topics`, and the repository's units) | derive each repository's roles (§forgejo/webhook/repository-roles) and whether it has the pull requests unit (§forgejo/reconciliation/the-sweep) |
| list open issues in a repo | event filtering and reconciliation enumeration |
| list every issue in a repo, either state | the outcomes view's walk, which splits current from past on each row's own state (§forgejo/observability/the-outcomes-view) |
| list open pull requests in a repo | event filtering, reconciliation enumeration, and the landing set's delivering-PR lookup (§forgejo/decisions/sync-set) |
| get a single issue (state, labels, author, `updated_at`, `html_url`) | authoritative issue state |
| get a single pull request (state, merged, head sha, `mergeable`, author, body, `html_url`) | authoritative PR state |
| list the issues an issue **blocks** (`issue-blocks`) | the unblock cascade, including cross-repo blockers |
| list the issues that block an issue (`issue-dependencies`) | the unblock cascade, including cross-repo blockers |
| list a pull request's reviews (event + author + commit) | the review transitions |
| list an issue's comments (author + body) | the forest's actionable-state digest, which folds comments by anyone other than the engine's own account or the configured decomposer identity (§forgejo/decisions/actionable-state-digest) |
| edit an issue's body (`body` only) | the one mutation: a tracker's current-state block (§forgejo/reconciliation/the-tracker-block) |

Issue responses also report **when the issue closed** (`closed_at`): the
close time a rendered chart stamps on a closed member
(§forgejo/renderer/closed-members). It is zero while the issue is open, and
`updated_at` is never used in its place — the last edit to a closed issue is
not the moment it closed.

Issue labels carry **the colour the forge reports for each** (`color`, a
six-digit hex without its `#`), mapped onto the issue as the client's
normalised `#rrggbb`: the stroke hexes of a rendered chart read from it, so a
label recoloured in the forge shows in the next chart
(§forgejo/renderer/classes-and-strokes, §forgejo/reconciliation/the-tracker-block).

Issue responses also report **who opened the issue** (`user.login`): the
author is carried on the issue model and read by nothing in the engine
— no decision compares it against any identity, since the authorship
guard was retired in favour of the `forest-seeding` hold-off
(§forgejo/decisions/transition-table, s-git/maitred#135). A response
without a `user` object yields an empty author.

Pull request responses likewise report **who opened the pull request**
(`user.login`): the author is carried on the PR model and read by the
`fix-feedback` ownership gate, which dispatches the action only when
the PR's author is the configured implementer identity
(§forgejo/config/implementer-identity, §forgejo/decisions/loop-prevention,
s-git/maitred#143). A response without a `user` object yields an empty
author, which with an identity configured reads as not owned.

Issue responses also report **whether the issue is a pull request** —
Forgejo represents pull requests as issues, and the API marks them with a
non-null `pull_request` object. The re-fetch uses this to tell connected PRs
apart from connected issues among blockers and blocks
(§forgejo/webhook/re-fetch).

Comment responses report only **who wrote the comment** (`user.login`)
and the comment's **body**: nothing else about a comment — its timestamps,
reactions, or the object it hangs off — is mapped, because the digest that
reads them counts a comment as its author and text alone
(§forgejo/decisions/actionable-state-digest). A response without a `user`
object yields an empty author.

Issue responses also report the issue's **body** (`body`). Two seats read
it: the landing set's revision digests the grouping issue's body, the
field that names the order its members must land in, so a restatement of
the order moves the digest while a comment or a label toggle does not
(§forgejo/decisions/sync-set), and the tracker-block write reads the
description it splices into (§forgejo/reconciliation/the-tracker-block).
The outcomes view draws its picture from the graph and reads no body at
all (§forgejo/observability/the-outcomes-view).

An issue arrives in the same shape whichever operation carried it: the
single-issue read, the dependency listing and the blocks listing all
answer with the issue model, `body` among its fields, and the forge
fills that field the same way for each — no operation hands back a stub
where another gives the description. A caller holding an issue's row
therefore holds its description, and reading it from that row costs no
second read of the same issue.

Pull request responses likewise report the pull request's **body**
(`body`). Two seats read it: the landing set's predicate resolves a
member's delivering PR through the auto-close reference the PR body
carries — the pull-request-side relation the forge honours by closing
the member when the delivery merges, where no blocking edge between
the member and its delivery exists to be read
(§forgejo/decisions/sync-set, s-git/maitred#171) — and the merge
opt-in's issue seat is the issue that same closing grammar names in
the body, so the body also gates every path that can dispatch
`merge-or-wait` (§forgejo/decisions/merge-gate, s-git/maitred#196).

The repository listing also reports **whether the repository has the
pull requests unit** (`has_pull_requests`). A repository with the unit
off refuses the pull request endpoints outright — the listing of its
pull requests answers `404` — and can hold no pull request, so the sweep
skips that listing where the unit is off
(§forgejo/reconciliation/the-sweep). The client deliberately does **not**
map a `404` to an empty listing: the general mapping would also swallow
the refusal that deserves an error — a renamed or deleted repository —
so where the unit is on a `404` stays an error.

The operations are exposed behind an **interface** so the decision function,
webhook handler, and reconciliation sweep can be tested against a fake.

## The one mutation

`UpdateIssueBody` sends `PATCH /repos/{owner}/{repo}/issues/{index}` carrying
`body` and nothing else. The narrowness is the point and is enforced by the
payload: a title, a state, a label set, a milestone and an assignment are
never sent, so they cannot change — and a body-only payload cannot reopen or
close the issue it rewrites.

The engine addresses only the tracker row of a current forest it enumerated
(§forgejo/reconciliation/the-tracker-block, §forgejo/observability/the-outcomes-view),
under the bearer token its reads already use: the account that authors the
write is the one `engine_account` names (§forgejo/config/engine-account), and
no second token, persona or endpoint exists for this write to fall back on.
Where that account has no write on an outcomes repository, the engine logs the
refusal and writes nothing; it does not substitute a comment, which would land
in the very digest the write exists to stay out of
(§forgejo/decisions/actionable-state-digest).

## Errors

A non-2xx response from the API is reported as an error carrying the HTTP
status code, so callers can distinguish a missing object (404) from a
transient failure.

## Configuration

Base URL, token, and org come from configuration/env. The client
authenticates every request with the bearer token.

The client may also present an **mTLS client certificate**. When the
environment sets both the certificate and key paths
(`MAITRED_FORGEJO_CLIENT_CERT` and `MAITRED_FORGEJO_CLIENT_KEY`), the client
loads the pair and presents the certificate on every request — a Forgejo
instance's ingress may require a client certificate. When neither is set the
client is plain, so non-mTLS Forgejo instances keep working; setting exactly
one of the two is a configuration error.

The pair is environment-derived and loads without contacting the instance,
so the classifier that names why the engine is off checks it under the same
name the startup path fails on (§forgejo/observability/why-the-engine-is-off,
§forgejo/webhook/configuration): setting exactly one of the two is the
environment the engine cannot serve — **the environment is incomplete**, and
the category is *credentials missing* — while a pair that is named but will
not load is a fault no named cause describes.
