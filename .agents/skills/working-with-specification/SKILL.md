---
name: working-with-specification
description: >-
  How to work with the project specification: it is the source of truth, is
  referenced via the § convention, and is changed spec-first in a red-then-green
  TDD fashion. Mandatory for any work touching the specification or code.
---

# Working with the specification

The specification lives in the `specification/` folder (at the repository root)
and is the **source of truth** for this project. This skill is **mandatory** for
any work that touches the specification or the implementation.

## The specification governs the implementation

- **Source of truth.** If the specification says so, the implementation must
  follow — never the other way around. When the two disagree, the specification
  wins and the implementation is brought into line.
- **Never ahead, never behind (lockstep).** The specification is changed first,
  and by the time an agent hands over for a merge proposal, the implementation
  reflects the specification again. At every merge point the two agree: the
  specification never describes behaviour the implementation does not have (ahead
  of it) and never omits behaviour the implementation has (behind it).
- **May be updated and slimmed.** If, during implementation, the specification
  has fallen out of sync or has gaps compared to what needs to happen, it may be
  updated (and trimmed) as part of the same change.

## Proposed specification changes

A **proposed specification change** is a change to the specification that has
been drafted and agreed with the human but not yet implemented. A proposed
change is **not** part of `specification/`: placing it there would put the
specification ahead of the implementation and break the lockstep invariant.
Proposed changes are carried outside `specification/` until the change that
implements them brings them in.

When the work ahead spans several specification changes — for example,
specifying a whole project from an empty repository — do not write the
specification all at once. Instead:

1. **Draft the proposed specification changes.** One per coherent slice of
   behaviour. Each states the exact specification text that will land in
   `specification/` (target file, heading, and the `§` reference it will have),
   together with what the implementation must do to satisfy it.
2. **Detail a plan.** The ordered sequence in which the proposed changes will be
   implemented, with the dependencies and reasoning behind the order.
3. **Present the plan and the proposed changes to the human** and revise them
   together until both are agreed — the same live conversation as any
   specification change (see the workflow below), not a document dropped for
   later.
4. **Let the human choose how the proposed changes are carried:**
   - **Chained issues (preferred).** Raise one issue per proposed change, in
     order. Each issue references its predecessor and successor so the chain can
     be followed, and notes where the plan lives (for example in the first issue
     of the chain, or a dedicated tracking issue). The issue body carries the
     full proposed specification change — **the issue is the authoritative ask**.
     Each pull request that closes an issue applies exactly that change to
     `specification/` and implements it red-then-green, so every pull request is
     semantically coherent and keeps the specification and the implementation in
     lockstep — and the repository is never polluted with specification sections
     that are not yet true (no confusing specification document that can lead an
     agent astray). If the repository's agentic pipeline supports label-based
     auto-merging (for example an `agentic-auto-merge` label on an issue or pull
     request, which an agentic reviewer honours by merging an approved pull
     request), label the issues so the chain can be driven autonomously.
   - **A separate folder (fallback).** Keep the proposed changes in a folder
     outside `specification/` (for example `specification-proposed/`), clearly
     marked as not yet authoritative. The lockstep invariant applies to
     `specification/`, which the separate folder does not touch. When a proposed
     change is implemented, its text moves into `specification/` and out of the
     proposed folder in the same change.

In both cases a proposed change enters `specification/` only in the same change
that implements it — never before.

A chained issue for a proposed specification change reads like this:

    ## Proposed specification change

    Part <k> of <N> — plan: <link to the first issue or tracking issue>
    Depends on: #<previous issue> · Unblocks: #<next issue>

    <the exact specification text that will land in specification/<path>.md
    under ## <Heading> — §<path>/<slug>>

    ## Acceptance

    The pull request that closes this issue must (1) apply the specification
    change above to `specification/` — this issue is the authoritative ask —
    (2) implement it red-then-green, and (3) keep `make build test lint` green.

## The `§` reference convention

Reference a location in the specification with:

    §<relative-path>/<heading-or-anchor-slug>

- `<relative-path>` — the file's path relative to `specification/`, without its
  `.md` extension. It may contain `/` to reach sub-folders (the specification is
  tiered).
- `<heading-or-anchor-slug>` — a Markdown heading converted to a slug, or an
  explicit HTML anchor id. The **final** `/` separates the path from the slug; a
  slug never contains `/`, so the split is unambiguous.

The slug follows the common (GitHub/GitLab) rule: lowercase the heading, replace
spaces with hyphens, and remove punctuation. See
[GitHub's autogenerated-header rules](https://docs.github.com/en/get-started/writing-on-github/getting-started-with-writing-and-formatting-on-github/basic-writing-and-formatting-syntax).

### Examples

| Reference | Resolves to |
|---|---|
| `§overview/scope` | `specification/overview.md`, heading `## Scope` |
| `§overview/non-goals` | `specification/overview.md`, heading `## Non-goals` |
| `§authentication/session/sessions` | `specification/authentication/session.md`, heading `## Sessions` |
| `§billing/pricing/pricing-tiers` | `specification/billing/pricing.md`, heading `## Pricing tiers` |

Use `§` references in the specification, in `AGENTS.md`, in commit messages, and
in code comments to keep the specification and the implementation linked.

## Workflow: specification first, agreed with the human, then red-then-green TDD

The specification is a **proposal you shape together with the human**, not a
document you write unilaterally. Spec-driven development only works because the
human actively participates in the specification — so treat them as a
collaborator, not an audience.

For every change:

1. **Update the specification first.** Before touching implementation code,
   change the specification to describe the desired behaviour. Reference
   specification locations with the `§` convention. When the work spans several
   specification changes, follow *Proposed specification changes* above instead
   of editing `specification/` directly.
2. **Present it and ask what needs rectifying.** Show the human the proposed
   specification (or the change to it) and ask whether anything needs
   rectifying. Frame it as a genuine proposal — "here is what I think we should
   specify; what would you change?" — and actually incorporate their feedback.
   Do not move on to implementation until they have had a chance to review it
   and are satisfied, or have explicitly waved it through.
   - **This is a conversation, not a status field.** Do not "present" the spec
     by dropping a file marked `status: proposal` (or similar) and moving on.
     Hold the human's hand: walk through it together, revise, and repeat until
     they are genuinely happy.
3. **Implement in a red-then-green TDD fashion.** For the specification you and
   the human have agreed on:
   - **Red** — write a failing test that expresses the new or changed behaviour.
     Confirm it fails for the right reason (missing code or wrong value, not a
     syntax error).
   - **Green** — write the minimum implementation to make the test pass.
4. **Keep them in lockstep.** When you are ready to hand over for a merge
   proposal, the implementation must reflect the specification. Commit only when
   you and the human are both ready — the commit is the shared signal that the
   specification is settled.
