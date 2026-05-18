# maitred — Periodic Trigger Engine

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
