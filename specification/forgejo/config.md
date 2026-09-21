# Forgejo config

The engine is configured **exclusively** through canned prompts: every named
action the decision function can dispatch (§forgejo/decisions/actions) is
mapped, in the config, to the canned prompt that implements it. The engine
code decides *when* to act; the config decides *what* is dispatched.

## Canned prompts

The config maps each action — `implement`, `reassess`, `review`, `re-review`,
`fix-feedback`, `merge-or-wait`, `rebase` — to:

- a **prompt template** — the canned prompt: the hotelier task invocation
  (e.g. the `/issue-implementer`, `/pr-reviewer`, `/pr-feedback-fixer`
  invocations) plus their pre-flight checks,
- a **persona** — the persona the task runs as (e.g.
  `s-autonomics-implementer`, `s-autonomics-reviewer`),
- a **timeout** — how long the task may run.

The config also names the **org** the engine tracks (the organisation whose
`maitred-enabled` repositories it watches), and sets the
**reconciliation interval** (`reconcile_interval`): how often the
reconciliation sweep runs (§forgejo/reconciliation/scheduling).

## Placeholders

Prompt templates are Go `text/template` templates. The engine fills the
placeholders at dispatch time, from the event and the re-fetched state:

| Placeholder | Value |
|---|---|
| `.Repo` | the affected repository's `owner/repo` full name |
| `.Kind` | `issue` or `pr` |
| `.Number` | the issue or pull request number |
| `.IssueURL` | the issue's `html_url` (empty for PR dispatches) |
| `.PRURL` | the pull request's `html_url` (empty for issue dispatches) |
| `.Sender` | the login of the user who caused the event |
| `.Action` | the dispatched action's name |

## Loading

The config is loaded from a file, or from a directory of `.yaml`/`.yml`
files merged in sorted order (an action may be defined in only one file),
located by `MAITRED_FORGEJOENG_DIR` (default
`/etc/maitred/forgejoeng.yaml`). The config is deployed by s-puppet.

## Validation

Loading **fails at load time** — mirroring the trigger `Validate()` — when:

- an action the decision function references has no entry (a missing prompt
  is a load error, never a runtime surprise),
- an entry's prompt, persona, or timeout is missing or not positive,
- `reconcile_interval` is missing or not positive,
- a prompt template does not parse, or references a placeholder that is not
  in the placeholder table above,
- the config names an action the decision function does not dispatch,
- the org is missing, or a directory's files disagree about it or about
  `reconcile_interval`,
- a directory's files define the same action twice.
