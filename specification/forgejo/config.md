# Forgejo config

The engine is configured through canned prompts: every named action the
decision function can dispatch (§forgejo/decisions/actions) is mapped, in the
config, to the canned prompt that implements it. The engine code decides
*when* to act; the config decides *what* is dispatched. Alongside the
prompts sit a small number of scalar keys — the org, the reconciliation
interval, and the optional **decomposer identity**
(§forgejo/config/decomposer-identity). Config is therefore no longer
exclusively canned prompts.

## Canned prompts

The config maps each action — `implement`, `reassess`, `review`, `re-review`,
`fix-feedback`, `merge-or-wait`, `rebase`, `decompose`, `wrap-up` — to:

- a **prompt template** — the canned prompt: the hotelier task invocation
  (e.g. the `/issue-implementer`, `/pr-reviewer`, `/pr-feedback-fixer`
  invocations) plus their pre-flight checks,
- a **persona** — the persona the task runs as (e.g.
  `s-autonomics-implementer`, `s-autonomics-reviewer`),
- a **timeout** — how long the task may run.

The config also names the **org** the engine tracks (the organisation
whose role-holding repositories it watches
(§forgejo/webhook/repository-roles)), and sets the
**reconciliation interval** (`reconcile_interval`): how often the
reconciliation sweep runs (§forgejo/reconciliation/scheduling).

## Decomposer identity

One optional key names the Forgejo identity that performs decomposition —
the persona the engine holds off on, and the author of forest members:

| Key | Required | Value |
|---|---|---|
| `decomposer_identity` | no | the login of the decomposer account, compared exactly (no fuzzy matching) |

The key is **optional**: with no identity configured, the authorship
hold-off is inert and everything else works. An unset optional key is never
a load error (§forgejo/config/validation). The key grants no privilege and
admits nothing by itself — it only names the account that authorship
decisions compare against. In a directory merge, files that set the key
must agree, exactly as with the org; a conflict is a load error.

Why a config key and not a repository topic: topics mark *repositories*
(the discovery vocabulary, exactly as `maitred-enabled` works), not users.
"Who authored this issue?" is a fact about an account, which the
repository-marking vocabulary cannot answer; and the identity must be the
operator's declaration, not something the engine infers.

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
- the org is missing, or a directory's files disagree about it, about
  `reconcile_interval`, or about `decomposer_identity`,
- a directory's files define the same action twice.

Optional keys are the exception: `decomposer_identity` unset is valid and
never a load error (§forgejo/config/decomposer-identity).
