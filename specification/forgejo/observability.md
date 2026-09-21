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
reasoning.

The event path and the reconciliation sweep log their decisions alike:
the sweep feeds the same decision function through the same pipeline
(§forgejo/reconciliation/the-sweep), so every decision is logged exactly
once, wherever it is made.

## The dashboard view

The web dashboard gains a **view of the tracked issues/PRs**: the
issues and pull requests the engine has made a decision on. For each,
the view shows:

- the **current state** — the object's state as last re-fetched (open
  or closed; merged, for pull requests),
- the **watermark** — the last dispatched action and the revision it
  was dispatched at (§forgejo/state/watermark), or none when nothing
  has been dispatched,
- the **last decision** — the most recent dispatch or hold-off, with
  its reason and the event that produced it.

The view is served by a dashboard API endpoint and rendered as a
section of the dashboard, which appears only when there is at least one
tracked issue or PR. The view reflects the engine's in-memory record of
decisions: it is empty when the engine is disabled or has made no
decisions yet, and it is cleared on restart (the watermark store is
unaffected, §forgejo/state/persistence).
