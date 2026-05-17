# maitre-d — Periodic Trigger Engine

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

**`make test`** (unifies `lint`, `test-coverage`, and `test-race`) is **mandatory** for all agents.
No changes may be committed without passing `make test`.

- `make lint` — runs `go vet` and `gofumpt` formatting check
- `make test-coverage` — runs all tests with coverage; generates `coverage.out` and `coverage.html`
- `make test-race` — runs all tests with the race detector

Aim to keep coverage above 80% for all new code paths.

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
- Use `os.MkdirTemp("", "maitre-d-*")` for per-session temp directories.
- Clean up temp directories with `os.RemoveAll()` when done.
