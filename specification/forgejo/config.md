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

The config maps each action — `implement`, `reassess`,
`fix-feedback`, `merge-or-wait`, `rebase`, `decompose`, `wrap-up`,
`teardown-forest`, `sync` — to:

- a **prompt template** — the canned prompt: the hotelier task invocation
  (e.g. the `/issue-implementer`, `/pr-merger`, `/pr-feedback-fixer`
  invocations) plus their pre-flight checks,
- an **identity** — the single declared Forgejo login the task runs as
  (§forgejo/config/identity-collapse),
- a **timeout** — how long the task may run.

Pi expands a prompt template only when the dispatched message **starts
with `/<template-name>`** — the first byte must be the slash. A prompt
that opens with bare words travels to the agent verbatim: the identity's
canned prompt is never invoked and its parameter values are never
filled, which is how a `decompose` dispatch once reached the decomposer
with no issue URL at all. Every prompt template therefore begins with
the slash invocation and passes the object of the dispatch as its
argument — `.IssueURL` for the issue-path actions, `.PRURL` for the
PR-path ones. Text the config must state besides the URL (the fan-out
cap below, for instance) rides along as further arguments, **quoted**.
Pi tokenises an invocation's arguments on whitespace, respecting quotes
only, and a template body renders just the placeholders it names, so
unquoted text arrives as a scatter of positional arguments a template
that reads `$1` for the URL never renders — the extra text would vanish
as quietly as a missing slash does. One pair of double quotes makes the
sentence one argument.

The template name is part of the contract. The identity's Pi prompt
directory must hold a template of that name, because Pi falls back to
passing the message verbatim when no template matches: a typo or a
missing command reproduces the silent no-expansion failure above, with no
error anywhere. This repository pins the names the deployment is expected
to follow; where a deployment uses others, it records them with the
config it deploys (§forgejo/config/loading).

| Action | Canned prompt |
|---|---|
| `implement` | `/issue-implementer` |
| `reassess` | `/issue-reassess` |
| `fix-feedback` | `/pr-feedback-fixer` |
| `merge-or-wait` | `/pr-merger` |
| `rebase` | `/pr-rebase` |
| `decompose` | `/issue-decomposer` |
| `wrap-up` | `/issue-wrapper` |
| `teardown-forest` | `/forest-teardown` |
| `sync` | `/set-lander` |

The `implement` prompt's pre-flight checks must, before creating any
branch or pull request, establish whether a connected pull request
already exists for the issue and, if one does, report and stay silent
rather than start a second delivery: the sweep re-dispatches `implement`
revision notwithstanding, so that check — not the watermark — is what
keeps a re-fire of a delivered implementation quiet
(§forgejo/reconciliation/the-state-is-the-watermark,
§forgejo/config/canned-prompts).

Reviews are configured separately, as the ordered lens list
(§forgejo/config/review-lenses); the retired `review` and `re-review`
entries have no row here, and `/pr-reviewer` is retired with them.

A prompt that asks for a URL the state cannot fill is a dispatch with no
object: the invocation would read `/issue-implementer ` and Pi reports
nothing over a command with no parameters. The engine therefore fails
such a dispatch at dispatch time rather than enqueueing it
(§forgejo/webhook/the-pipeline) — loud where a mis-set state would
otherwise be silent. It is a dispatch-time rule, not a load-time one: it
catches the state that carries no object without ever refusing to start
over a config that has yet to be rewritten (§forgejo/config/validation).

The `decompose` prompt template is held to one content requirement: it
must **name the fan-out cap** — at most 20 tickets per forest — and
require the agent to **cite the cap when refusing** to fan out further
(a root whose forest would exceed it must be refused by name, not
silently trimmed). The cap itself belongs to the outcomes design, not
to the engine; what the config must guarantee is that the agent is
told the number and is told to cite it.

The config also names the **org** the engine tracks (the organisation
whose role-holding repositories it watches
(§forgejo/webhook/repository-roles)), sets the
**reconciliation interval** (`reconcile_interval`): how often the
reconciliation sweep runs (§forgejo/reconciliation/scheduling), and
carries the ordered **review lens list** (§forgejo/config/review-lenses).

## Review lenses

Reviews are not one action with one prompt: the config declares an
ordered list of **review lenses**, each with its own identity, canned
prompt, and timeout:

```yaml
reviews:
  - identity: reviewer-minimalism
    prompt: '/pr-minimalism-reviewer {{.PRURL}}'
    timeout: 30m
  - identity: reviewer-maintainability
    prompt: '/pr-maintainability-reviewer {{.PRURL}}'
    timeout: 30m
  - identity: reviewer-security
    prompt: '/pr-security-reviewer {{.PRURL}}'
    timeout: 30m
```

Extending or contracting the chain is an edit here, not a code change.
The list is **ordered**, and the order is the chain's: the first lens
biases the implementation before the later ones see it, and because
approvals do not survive a push, complexity added to satisfy a later
lens comes back through the earlier ones (§forgejo/decisions/review-lenses).

`identity` is a **declaration in the operator's voice**, the same
standing as `implementer_identity` and `decomposer_identity`: the
decision function compares it against review authors by exact,
case-sensitive equality to decide which lenses owe a verdict, and the
engine never infers the roster from the reviews it reads. It grants no
privilege **on the forge** — a lens's rights live there, not in this
key. It is not privilege-free in the deployment, though: this key is
also the persona name the queue resolves, so it decides whose
environment variables and file copies land in the task's working
directory, and the login it names must not be an actor's
(§forgejo/config/identity-collapse). An audit of which config keys name
credentials therefore reads every `identity` in the file, not just the
ones spelled `persona` elsewhere.

Per lens: `identity` and `prompt` are required, `timeout` must be
positive, and the prompt is held to the same canned-prompt
contract as an action's. A lens identity appears at most once; in a
directory merge the lists concatenate in sorted file order and a
repeated identity is a conflict, exactly as with the scalar keys. A lens
identity must also be **no actor's login**: it may not equal
`implementer_identity`, `decomposer_identity`, or the `identity` of any
action entry, because one declared name is both what the decisions
compare a review author against and the persona the task runs as — a
lens naming an actor would let that account satisfy its own gate
(§forgejo/config/identity-collapse). Repeated and colliding identities
are plain validation failures, not the config-or-binary mismatch: the
binary and the config agree about the schema; the deployment wrote a
config that contradicts itself.
At least one lens is required (§forgejo/config/validation): with no
lenses nothing reviews, and a chain of zero would silently pass every
gate that reads the chain.

## Identity collapse

Every entry that declares a prompt — each action and each review lens —
declares **exactly one** name: `identity`, whose value is the Forgejo
login. There is no second key.

Before the collapse an entry could carry two: `identity`, the login the
decisions compare, and `persona`, the name handed to the queue system
and thence to hotelier. Two keys holding one string by convention alone
are a fact about the deployment rather than about the schema — they can
disagree while nothing anywhere reports anything wrong — so the pair is
collapsed into the one field the login belongs in. What the engine
enqueues as the task's persona name is now the entry's declared login,
verbatim, and downstream consumers key on that login and on nothing
else: never a derived value such as an email hash, which moves when an
email is regenerated while the login stands still.

The collapse is a **schema change validated as an exact set of keys in
both directions**, the same stance the action set already takes: an entry
with no `identity` fails the load as a missing required field — the field
is never defaulted — and a key the binary does not declare fails it,
naming that key. The full list of load-time failures, and the category a
key the binary does not declare classifies into, belong to
§forgejo/config/validation.

One rule the collapse itself puts on the load: **the login whose verdict
a gate counts must not be a login the config declares as an actor**; the
enumeration of those logins, and the refusal of a lens identity equal to
one, belong to §forgejo/config/review-lenses. Before the collapse that
collision needed two coincidences to matter; with one name that is
simultaneously what the decisions compare review authors against and the
persona the task is materialised with, one mistyped lens entry would hand the
implementing account a gate whose whole job is to be somebody else's verdict.

**The name travels, so the pairing downstream must fail closed.** The
persona name the queue system receives is read off a key the operator
thinks of as a login, so renaming or recreating that account silently
re-points every task in the role at a different persona — a different set
of environment variables and file copies on the runner. The engine cannot
ask whether a persona of that name exists. What makes the coupling safe
is one deployment-side property: a task whose persona name resolves to
nothing must be refused, never run under the runner's own identity. That
property belongs to the deployment, not to this binary; it is stated here
so the failure mode has a home in the specification.

**The deployed config must land in the same window as the binary.** A
deployment that ships this binary over a config that still carries
`persona:` does not start — loudly, with the mismatch named — and a
config written for this binary run by the older one does not start
either, but as *other fault* for want of a persona: only the newer binary
has the key-set check that names the mismatch. The engine-side half lives
here; the deployment half is s-puppet's, and the two land together as the
landing set named on the forest's tracker. In the meantime the in-repo
sample (`config/forgejoeng.yaml`) carries the new shape, so anything
built from this repository is already speaking it.

What the collapse does **not** touch: the queue payload's `persona`
field, which is the queue system's name for the actor and now simply
carries the login; the trigger config's `persona`, which belongs to the
trigger subsystem and never named a forge account
(`specification/overview.md`); and the decision comparisons, which read the
same declared logins they always did.

## Decomposer identity

One optional key names the Forgejo identity that performs decomposition —
the author of forest members:

| Key | Required | Value |
|---|---|---|
| `decomposer_identity` | no | the login of the decomposer account; read by one row alone — a comment on an open `ideation` root authored by this identity holds off, any other author's dispatches `decompose` (§forgejo/decisions/transition-table, §forgejo/decisions/loop-prevention, s-git/maitred#147); issue authorship still gates nothing (s-git/maitred#135) |

The key is **optional**: an unset optional key is never a load error
(§forgejo/config/validation). The key grants no privilege and admits
nothing by itself — it names the account that composes forests; the
engine retains it as the operator's declaration of who composes forests
and reads it in exactly one place: the author guard on a comment at an
open `ideation` root, where the decomposer's own comment must not
re-trigger the decomposer (§forgejo/decisions/loop-prevention,
s-git/maitred#147). The guard compares the delivery's `sender.login`
against the declared key by **exact, case-sensitive equality** — the
same rule the `fix-feedback` ownership gate applies to
`implementer_identity` below. A declaration that does not match the
account that actually posts (a stale key, a renamed account) leaves
the guard silently fail-open, so it is the operator's responsibility
to keep the declared login current; the dispatch reason on the comment
row names the comment's author, so such a mis-declaration is legible
in the decision log. No other decision compares it — what gates a
forest member is still the `forest-seeding` hold-off, and issue
authorship gates nothing (s-git/maitred#135). In a directory merge,
files that set the key must agree, exactly as with the org; a conflict
is a load error.

Why a config key and not a repository topic: topics mark *repositories*
(the discovery vocabulary, exactly as `maitred-enabled` works), not users.
"Who authored this issue?" is a fact about the account, which the
repository-marking vocabulary cannot answer; and the identity must be the
operator's declaration, not something the engine infers. The key records
who composes forests; it gates no work item — only the author row of a
root's comment (s-git/maitred#135, s-git/maitred#147).

## Implementer identity

One optional key names the Forgejo identity that implements work and
fixes review feedback:

| Key | Required | Value |
|---|---|---|
| `implementer_identity` | no | the login of the implementer account; the `fix-feedback` dispatch compares the PR's author against it — a PR authored by anyone else holds the action off (§forgejo/decisions/loop-prevention, s-git/maitred#143) |

The key is **optional**: an unset optional key is never a load error
(§forgejo/config/validation). Unset, the ownership gate is **inert**:
`fix-feedback` dispatches exactly as before, whatever the PR's author
— a missing declaration is not treated as a gate. In a directory
merge, files that set the key must agree, exactly as with the org and
the decomposer identity; a conflict is a load error.

Why a config key follows the decomposer identity's reasoning: topics
mark repositories, not users, and the gate must be the operator's
declaration, not something the engine infers. The two keys name
different roles: the decomposer composes forests and its identity
guards one row alone — a comment at a root (s-git/maitred#135,
s-git/maitred#147); the implementer implements and fixes, and its
identity holds `fix-feedback` off the pull requests it does not own —
the churn of an implementer dispatched onto a human's PR, answering
reviews the human is handling themselves, is what the gate prevents
(s-git/s-puppet#118, s-git/maitred#143).

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
- an entry's identity or prompt is missing, or its timeout is not positive
  (§forgejo/config/identity-collapse),
- the config carries a key the binary does not declare at that depth — the
  retired `persona` among them, refused by name
  (§forgejo/config/identity-collapse),
- `reconcile_interval` is missing or not positive,
- a prompt template does not parse, or references a placeholder that is not
  in the placeholder table above,
- the config names an action the decision function does not dispatch,
- the org is missing, or a directory's files disagree about it, about
  `reconcile_interval`, about `decomposer_identity`, or about
  `implementer_identity`,
- a directory's files define the same action twice.

The lens list is validated with the actions: it must hold at least one
lens, every lens must carry its one declared identity and a prompt with a
positive timeout, identities must be unique and none may be a login an
actor already declares (§forgejo/config/review-lenses), and a lens prompt
is parsed like an action's (§forgejo/config/review-lenses). A config that
still names the retired `review` or `re-review` action entry fails the
unknown-action check, and a config with no `reviews:` list fails the
empty-list check: either way the failure is the config-or-binary
mismatch (§forgejo/config/validation) — the loud half of the
config-before-binary rule, telling the deployment to write the lens
list before the binary that retired the singletons is deployed.

Optional keys are the exception: `decomposer_identity` and
`implementer_identity` unset are valid and never a load error
(§forgejo/config/decomposer-identity, §forgejo/config/implementer-identity).

The two exact-set failures — an entry missing for an action the binary
knows, and an entry the binary does not know — are one named cause, the
**config-or-binary mismatch**, distinguishable from every other load
failure without matching text. It is the signature of half a
config-and-binary change having landed, and surfaces must be able to say
so (§forgejo/observability/why-the-engine-is-off). A failure that is
neither that mismatch nor one of the loading kinds — no config at the
location, a config that cannot be read or parsed
(§forgejo/config/loading) — is a third thing: a fault, but not one of the
named ones.

Loading fails **closed**, and that fixes the upgrade ordering: a binary
that references a new action requires every deployment's config to
carry that entry **first**. Extending the enum is therefore a
config-before-binary change — a deployment whose config has not gained
the entry does not start, which is the loud failure; a prompt that
merely went missing at runtime would be the quiet one.

Shrinking the enum is the mirror image, and the same rule: a config
that still names an action the binary no longer dispatches fails the
unknown-action check above, so the deployment's config must **stop**
naming the action before the binary that has forgotten it is deployed
(s-git/maitred#151).
