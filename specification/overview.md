# Overview

The top-level specification. It states what the project is, what it is not, and
links into the per-area specification files in the sub-folders below.

## Purpose

Maître d' (maitred) is a periodic trigger engine that schedules and dispatches
tasks into a queue system. It reads trigger definitions from YAML files,
evaluates prompt templates against execution state and webhook payloads, and
pushes the resulting tasks to a configurable queue provider. It is designed to
run alongside [hotelier](https://github.com/skrewz/hotelier) but works with any
HTTP queue system.

## Scope

- **Periodic triggers.** Triggers are defined in YAML files (loaded
  recursively from the trigger directory) with a cron or `@every <duration>`
  schedule. Each trigger is evaluated on its own ticker and dispatches a task
  when its schedule fires. Cron schedules may be evaluated in a fixed
  timezone (`MAITRED_CRON_TZ`).
- **Prompt templating.** A trigger's `prompt` is a Go `text/template` with
  `.LastRun` (RFC 3339 timestamp of the trigger's last execution) and
  `.Payload` (the webhook payload, when present), plus custom template
  functions (`TrimSuffix`, `index`, `len`).
- **Hold-off conditions.** A trigger may declare a singular `hold-off-condition`
  and/or a `hold-off-conditions` list of Go template conditions. If any
  evaluates to `true` at evaluation time, the trigger is held off (no dispatch)
  and the matching conditions are logged.
- **Webhook ingestion.** Named webhook endpoints (YAML files in the webhook
  directory) expose `POST /v1/{provider}/{endpoint}` on the API port. A
  received payload is mapped to a trigger and made available to its prompt
  template as `.Payload`.
- **Queue dispatch.** Tasks are dispatched through the `TaskQueueProvider`
  interface: an HTTP queue adapter (optional mTLS, optional custom task
  template, internal tracking ID appended to the prompt) or, without a queue
  config, an in-memory queue. Tasks carry a prompt, capability tags, a timeout,
  and an optional persona.
- **Persistent state.** Each trigger's last run time and extra state is
  persisted as a JSON file per trigger under the data directory and survives
  restarts. Per-trigger execution history is recorded.
- **Web dashboard.** A web UI on the web port shows trigger cards (schedule,
  state, countdown), stats, and interactive controls (fire now, pause/resume).
- **Health check.** `maitred --health` exits 0 if the configuration is valid.
- **Forgejo engine (new domain).** In scope but not yet implemented: a
  stateful webhook component that will track issue/PR state across the
  organisation's `maitred-enabled` repositories and dispatch canned hotelier
  tasks when a tracked transition warrants it, reconciling periodically so
  missed webhooks are recovered. To be specified in `specification/forgejo/`
  as the Forgejo engine chain (issues #48–#54, end-state #46) is implemented.

## Non-goals

- **Not a queue system.** maitred dispatches tasks to an external queue; it
  does not execute tasks or track their completion beyond its own dispatch
  state.
- **Not a general workflow engine.** There are no dependency graphs, DAGs, or
  multi-step task orchestration; a trigger produces at most one task per
  firing.
- **No Forgejo mutation.** The Forgejo engine will talk to Forgejo through a
  read-only client; its side effects will be dispatched tasks, never direct
  writes to Forgejo.
- **No built-in secrets management.** Tokens and certificates come from the
  environment or the filesystem (e.g. via s-puppet), not from maitred.

## Structure

The specification is organised into sub-folders, one per domain or feature. Add
a row (and a sub-folder) as each area is specified. References use the `§`
convention — see the `working-with-specification` skill.

| Area | Specification | Notes |
|---|---|---|
| Forgejo engine | `specification/forgejo/` | Per-change specs land here as the Forgejo engine chain is implemented; see `specification/forgejo/README.md` |

## References

- [`../AGENTS.md`](../AGENTS.md) — working agreements; makes the
  `working-with-specification` skill mandatory.
- [`README.md`](README.md) — the structure of this folder.
- [`../README.md`](../README.md) — the project README (user-facing
  documentation of the current implementation).
