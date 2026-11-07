# Forgejo client

The engine talks to Forgejo through a **read-only** client that exposes
exactly the operations the decision function and the event/reconciliation
paths need. It has **no mutation operations**: the engine's side effects are
dispatched tasks, never direct writes to Forgejo (§overview/non-goals).

## Operations

The client exposes these read operations over the Forgejo REST API v1,
authenticated with a bearer token:

| Operation | What the engine does with it |
|---|---|
| list an org's repositories (with `topics`, and the repository's units) | derive each repository's roles (§forgejo/webhook/repository-roles) and whether it has the pull requests unit (§forgejo/reconciliation/the-sweep) |
| list open issues in a repo | event filtering and reconciliation enumeration |
| list open pull requests in a repo | event filtering and reconciliation enumeration |
| get a single issue (state, labels, author, `updated_at`, `html_url`) | authoritative issue state |
| get a single pull request (state, merged, head sha, `mergeable`, author, `html_url`) | authoritative PR state |
| list the issues an issue **blocks** (`issue-blocks`) | the unblock cascade, including cross-repo blockers |
| list the issues that block an issue (`issue-dependencies`) | the unblock cascade, including cross-repo blockers |
| list a pull request's reviews (event + author + commit) | the review transitions |

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

Issue responses also report the issue's **body** (`body`). One seat reads
it: the landing set's revision digests the grouping issue's body, the
field that names the order its members must land in, so a restatement of
the order moves the digest while a comment or a label toggle does not
(§forgejo/decisions/sync-set).

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
