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
object carrying two things: the tracked list, and **one engine-status
field** rendering the engine's state in the words of the category enum
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
like a healthy one with nothing tracked. The view reflects the
engine's in-memory record of decisions: the tracked list is empty when
the engine is disabled or has made no decisions yet, and it is cleared
on restart (the watermark store is unaffected, §forgejo/state/persistence).

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
