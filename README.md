# Maître d'

> Periodic trigger engine for autonomous task queues.

Maître d' schedules and dispatches tasks into any queue system based on
periodic cron or duration-based triggers. It reads trigger definitions
from YAML files, evaluates prompt templates with execution state, and
pushes resulting tasks to a configurable queue provider.

Designed to run alongside [hotelier](https://github.com/skrewz/hotelier)
but works with any HTTP queue system. See the [Queue Adapter](#queue-adapter)
section for configuration details.

**Warning**: This is alpha grade software. Mostly made to scratch an itch of
mine. Use at your own risk—but let me know if you do.

## Screenshots

![Maître d' Web Dashboard](/docs/screenshot.png)

## Quick Start

```bash
# Build
make build

# Configure triggers
mkdir -p config/triggers.d data
# Edit config/triggers.d/*.yaml with your trigger definitions
# (or organize into subdirectories like config/triggers.d/periodic/, config/triggers.d/webhook/)

# Run
./bin/maitred
```

## Configuration

Triggers are defined as YAML files in `config/triggers.d/` (or any directory
set via `MAITRED_TRIGGER_DIR`). Files are loaded recursively from the
trigger directory and all subdirectories, processed in sorted order by full
path, enabling modular and organized configuration.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| Variable | Default | Description |
|----------|---------|-------------|
| `MAITRED_TRIGGER_DIR` | `config/triggers.d` | Directory containing trigger YAML files (loaded recursively) |
| `MAITRED_DATA_DIR` | `data` | Directory for persistent trigger state |
| `MAITRED_QUEUE_CONFIG` | — | Path to queue adapter YAML config (enables HTTP queue adapter) |
| `MAITRED_WEB_PORT` | `9090` | Port for the web dashboard |
| `MAITRED_API_PORT` | `9091` | Port for the webhook API |
| `MAITRED_WEBHOOK_DIR` | `config/webhook-endpoints.d` | Directory containing webhook endpoint YAML files |

### Trigger Definition Format

```yaml
triggers:
  - id: "my-trigger"
    type: periodic
    schedule: "0 */6 * * *"
    prompt: "Research since {{ .LastRun }}"
    repos:
      - "~/repos/hotelier"
    tags:
      - "business-default"
    timeout: 3600
```

| Field | Required | Description |
|-------|----------|-------------|
| `id` | ✅ | Unique trigger identifier |
| `type` | ✅ | Currently only `periodic` is supported |
| `schedule` | ✅ | Cron expression or `@every <duration>` |
| `prompt` | ✅ | Go template with `.LastRun` and `.Payload` variables |
| `repos` | | Repository paths for the task |
| `tags` | | Agent capability tags for routing |
| `timeout` | | Task timeout in seconds (0 = unlimited) |

### Schedule Formats

**Duration-based** (simple, relative):
```yaml
schedule: "@every 1h"
schedule: "@every 30m"
schedule: "@every 6h"
```

**Cron** (precise, absolute):
```yaml
schedule: "0 */6 * * *"     # Every 6 hours at minute 0
schedule: "@daily"           # Midnight every day
schedule: "@hourly"          # Top of every hour
```

### Queue Adapter

Maître d' dispatches tasks to a remote queue system via an HTTP adapter.
Configure it with a YAML config file and point to it via `MAITRED_QUEUE_CONFIG`
or the `--queue-config` flag.

**Example** — `config/queue.yaml`:

```yaml
# Target queue system endpoint
endpoint: "https://hotelier.example.com:8080"

# Optional mTLS client authentication
# Both fields must be set together — omit for no TLS auth
mtls_cert: "/path/to/client.crt"
mtls_key:  "/path/to/client.key"

# Optional: custom task body template (Go text/template syntax)
# Available fields: .ID, .Prompt, .Repos, .Tags, .Timeout
# A built-in "json" function marshals values to JSON for safe embedding.
# If omitted, the default template is used (see below).
task_template: |
  {
    "prompt": {{ .Prompt | json }},
    "repos": {{ .Repos | json }},
    "tags": {{ .Tags | json }},
    "timeout": {{ .Timeout }}
  }

# Optional: log the full response body from the remote system
log_response: false
```

**Default task template** (used when `task_template` is omitted):

```yaml
task_template: |
  {
    "prompt": {{ .Prompt | json }},
    "repos": {{ .Repos | json }},
    "tags": {{ .Tags | json }},
    "timeout": {{ .Timeout }}
  }
```

The default template omits the `id` field — the remote system is expected
to generate its own. The adapter injects an internal tracking ID into the
end of the prompt (`\n(internal maître d' tracking ID: <id>)`), enabling
reverse-tracing of tasks across systems.

**mTLS authentication** is optional. When both `mtls_cert` and `mtls_key`
are set, the adapter uses TLS client certificate authentication during the
handshake. Only the client certificate is required — system CAs are trusted
for server verification (no separate CA cert needed). When neither is set,
plain HTTP or server-only TLS (if the endpoint uses `https://`) is used.

> **Note**: The queue adapter is a core feature of maître d'. Without a queue
> config, the engine falls back to an in-memory queue that stores tasks in RAM
> only — tasks are never dispatched externally. For production use, always
> configure a queue adapter.

### Webhook Endpoints

Webhook events from external systems (e.g. Forgejo) can trigger existing
triggers by placing YAML files in `config/webhook-endpoints.d/`. Each file
becomes a named provider (filename without extension), and the webhook API
serves them at `/v1/{provider}/{endpoint}`.

**Example** — `config/webhook-endpoints.d/forgejo.yaml`:

```yaml
endpoints:
  - name: "pull_request"
    trigger_id: "pr-review"
    response: '{"status": "submitted"}'
```

This exposes the endpoint `POST /v1/forgejo/pull_request`. When Forgejo
sends a webhook, the raw JSON payload is available in the trigger's prompt
template as `.Payload`:

```yaml
triggers:
  - id: "pr-review"
    type: periodic
    schedule: "@every 1h"
    prompt: "Review PR {{ .Payload.pull_request.title }} since {{ .LastRun }}"
```

Provider names must consist only of `[a-z0-9-]` characters. The `.Payload`
template variable works with nested JSON — you can access deeply nested fields
via dot notation (e.g. `{{ .Payload.sender.login }}`).

The webhook API runs on a separate port from the web dashboard (default `9091`,
configurable via `MAITRED_API_PORT`). TLS termination is expected to be
handled by a reverse proxy in front of the API port.

## Architecture

```
┌─────────────────────────────────────────────────┐
│                    Maître d'                    │
│                                                 │
│  config/triggers.d/   pkg/engine/   pkg/state/  │
│  ┌──────────────┐    ┌──────────┐   ┌────────┐  │
│  │ YAML files   │───►│ Engine   │──►│ JSON   │  │
│  │ (parsed at   │    │ (cron    │   │ files  │  │
│  │  startup)    │    │  ticker  │   │ per    │  │
│  └──────────────┘    │  goroutine│  │ trigger│  │
│                      └────┬─────┘   └────────┘  │
│                           │                     │
│                           ▼                     │
│                    ┌──────────────┐             │
│                    │ TaskQueue    │             │
│                    │ Provider     │             │
│                    └──────┬───────┘             │
└───────────────────────────┬─────────────────────┘
                            │
                            ▼
                  ┌──────────────────┐
                  │  Queue System    │
                  │ (hotelier, etc.) │
                  └──────────────────┘

  ┌─────────────────────────────────────────────────┐
  │              Webhook API (port 9091)            │
  │                                                 │
  │  config/webhook-endpoints.d/  pkg/webhook/      │
  │  ┌──────────────┐    ┌────────────┐             │
  │  │ YAML files   │───►│ Handler    │──► Engine   │
  │  │ (parsed at   │    │ /v1/{      │   (reuses   │
  │  │  startup)    │    │  provider} │   prompt    │
  │  └──────────────┘    │  /         │   engine)   │
  │                      │  endpoint  │             │
  │                      └────────────┘             │
  └─────────────────────────────────────────────────┘
```

```
┌─────────────────────────────────────────────────┐
│                    Maître d'                    │
│                                                 │
│  config/triggers.d/   pkg/engine/   pkg/state/  │
│  ┌──────────────┐    ┌──────────┐   ┌────────┐  │
│  │ YAML files   │───►│ Engine   │──►│ JSON   │  │
│  │ (parsed at   │    │ (cron    │   │ files  │  │
│  │  startup)    │    │  ticker  │   │ per    │  │
│  └──────────────┘    │  goroutine│  │ trigger│  │
│                      └────┬─────┘   └────────┘  │
│                           │                     │
│                           ▼                     │
│                    ┌──────────────┐             │
│                    │ TaskQueue    │             │
│                    │ Provider     │             │
│                    └──────┬───────┘             │
└───────────────────────────┬─────────────────────┘
                            │
                            ▼
                  ┌──────────────────┐
                  │  Queue System    │
                  │ (hotelier, etc.) │
                  └──────────────────┘
```

## CLI Flags

| Flag | Description |
|------|-------------|
| `--trigger-dir` | Directory containing trigger YAML files (overrides `MAITRED_TRIGGER_DIR`) |
| `--data-dir` | Directory for persistent trigger state (overrides `MAITRED_DATA_DIR`) |
| `--queue-config` | Path to queue adapter YAML config (overrides `MAITRED_QUEUE_CONFIG`) |
| `--web-port` | Port for the web dashboard (overrides `MAITRED_WEB_PORT`) |
| `--api-port` | Port for the webhook API (overrides `MAITRED_API_PORT`) |
| `--webhook-dir` | Directory containing webhook endpoint YAML files (overrides `MAITRED_WEBHOOK_DIR`) |
| `--version` | Print version and exit |
| `--health` | Health check mode (exits 0 if config is valid) |

## Development

```bash
make test           # Run all tests (lint + coverage + race)
make lint           # go vet + gofumpt check
make test-coverage  # Tests with coverage report
make test-race      # Tests with race detector
make build          # Build binary
make clean          # Remove build artifacts
```

See [AGENTS.md](AGENTS.md) for project conventions.

## License

MIT
