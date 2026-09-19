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
| list an org's repositories (with `topics`) | find the `maitred-enabled` repos |
| list open issues in a repo | event filtering and reconciliation enumeration |
| list open pull requests in a repo | event filtering and reconciliation enumeration |
| get a single issue (state, labels, `updated_at`, `html_url`) | authoritative issue state |
| get a single pull request (state, merged, head sha, `mergeable`, `html_url`) | authoritative PR state |
| list the issues an issue **blocks** (`issue-blocks`) | the unblock cascade, including cross-repo blockers |
| list the issues that block an issue (`issue-dependencies`) | the unblock cascade, including cross-repo blockers |
| list a pull request's reviews (event + author + commit) | the review transitions |

Issue responses also report **whether the issue is a pull request** —
Forgejo represents pull requests as issues, and the API marks them with a
non-null `pull_request` object. The re-fetch uses this to tell connected PRs
apart from connected issues among blockers and blocks
(§forgejo/webhook/re-fetch).

The operations are exposed behind an **interface** so the decision function,
webhook handler, and reconciliation sweep can be tested against a fake.

## Errors

A non-2xx response from the API is reported as an error carrying the HTTP
status code, so callers can distinguish a missing object (404) from a
transient failure.

## Configuration

Base URL, token, and org come from configuration/env. The client
authenticates every request with the bearer token.
