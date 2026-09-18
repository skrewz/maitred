# Maître d' — Periodic Trigger Engine

## Mandatory: working with the specification

All development work in this repository **must** follow the
[`working-with-specification` skill](.agents/skills/working-with-specification/SKILL.md).
The specification lives in [`specification/`](specification/) and is the
**source of truth**: the implementation follows it, never the reverse. The
skill defines how to reference the specification with the `§` convention, the
spec-first, red-then-green TDD workflow, and how proposed (not yet implemented)
specification changes are carried — as chained issues (preferred) or in a
separate folder — so that the specification is never ahead of or behind the
implementation. Do not work around it.

## General gotcha's for agents

- You will likely be working with a git worktree. Please orient yourself.
- Avoid accessing /tmp/ for temporary files. Use a temporary folder within the directory (and clean up) instead.
- Be podman-centric. Docker is not used here.
- Check if there are Makefile targets for building and/or linting; use them before handing over the task.
- When handling errors, make changes only if they relate to the specific error
- Proactively make use of web search tools for documentation, examples, versions etc
- If producing git commits, use gitmoji and conventional commits
- Under *no circumstances* are you allowed to push git commits.
- Do not use complete paths in the read tool if a relative path would do.
- Always load any web search skills available to you. They will almost always be relevant to your work.

## Public mirror — what must not leak

This repository is mirrored to GitHub, so **everything committed becomes
public**. Keep internal Forgejo and infrastructure details out of commit
messages, PR titles/bodies, and — secondarily — code, config and docs:

- **No internal hosts.** Never reference `forgejo.skrewz.net` (or any other
  internal host) in commit messages, PR titles/bodies, or code/docs. Link
  issues and PRs by number only (e.g. `Fixes #22`), not by full URL.
- **No internal identities.** Do not put internal identities (`@skrewz.net`
  email addresses, personal names) in commit metadata or messages.
- **No personal asides.** Keep commit and PR bodies free of personal asides.
- **Neutral naming.** Prefer neutral, generic names in code, config and
  examples over internal project or persona names. Where a specific
  integration provider is involved, do not make its naming (provider names,
  API paths) the canonical example where a generic one will do.
- **Known limitation: merge trailers.** Forgejo auto-merge trailers
  (`Reviewed-on: …`, `Reviewed-by: …`) are added automatically by the forge
  and carry the internal host. This cannot be avoided from the client side;
  a follow-up (see #57) should strip or rewrite them before mirroring.

## Mandatory test and lint targets

**`make test`** (unifies `lint`, `test-coverage`, `test-race`, and `test-ui`) is **mandatory** for all agents.
No changes may be committed without passing `make test`.

- `make lint` — runs `go vet` and `gofumpt` formatting check
- `make test-coverage` — runs all tests with coverage; generates `coverage.out` and `coverage.html`
- `make test-race` — runs all tests with the race detector
- `make test-ui` — runs Playwright headless browser tests against the web dashboard

Aim to keep coverage above 80% for all new code paths.

## Web UI changes

When modifying the web UI (`pkg/web/static/index.html`), always update the
Playwright tests in `pkg/web/ui_test.mjs` to cover any new or changed behaviour.
The tests are run automatically as part of `make test`, so if a test is broken
the build will fail.

Key test areas to keep in mind:
- Page structure (title, header, stats, cards, metadata labels)
- Data rendering (trigger cards, schedules, badges, countdowns)
- Interactive controls (Fire now, Pause/Resume toggle)
- SPA fallback, API error handling, and HTTP method enforcement

### UI screenshots

The README includes a screenshot of the dashboard at `docs/screenshot.png`.
When making UI changes, **consider** whether the screenshot should be updated.
Do this by comparing the old and new screenshots side by side:

1. Copy the existing screenshot to a temporary folder: `cp docs/screenshot.png /tmp/old-screenshot.png`
2. Start a maitred instance, take a fresh screenshot with Playwright
3. **Read both screenshots** and compare them visually
4. Decide whether the change is significant enough to warrant an update:
   - **Update it** if the change is visually significant (new sections, layout
     shifts, new controls, major colour/typography changes)
   - **Skip it** for minor tweaks (bug fixes, small label changes, edge-case
     handling) that don't alter the overall look of the dashboard
5. Clean up: remove the temp files

If you decide to update it, replace `docs/screenshot.png`.

## Go formatting

- Run `make format` (gofumpt) before committing changes. gofumpt is stricter than `go fmt`.
- Run `make check-format` to verify formatting is correct.
- If gofumpt is not installed, install it with: `go install mvdan.cc/gofumpt@latest`

## Code coverage

- Run `make test-coverage` to generate a coverage report.
- When adding or modifying tests, ensure you are maintaining or improving coverage.
- Coverage output is written to `coverage.html` (HTML) and `coverage.out` (text).
- The `make test-coverage` target prints the total coverage percentage to stdout.
- Aim to keep test coverage above 80% for all new code paths.

## Agent working directory

- When the agent starts, it creates a temporary directory for working files.
- Use `os.MkdirTemp("", "maitred-*")` for per-session temp directories.
- Clean up temp directories with `os.RemoveAll()` when done.
