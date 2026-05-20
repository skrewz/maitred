# Maître d'

> Periodic trigger engine for autonomous task queues.

Maître d' schedules and dispatches tasks into any queue system based on
periodic cron or duration-based triggers. It reads trigger definitions
from YAML files, evaluates prompt templates with execution state, and
pushes resulting tasks to a configurable queue provider.

Designed to run alongside [hotelier](https://github.com/skrewz/hotelier)
but works with any system that implements the `TaskQueueProvider` interface.

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

# Run
./bin/maitred
```

## Configuration

Triggers are defined as YAML files in `config/triggers.d/` (or any directory
set via `MAITRE_D_TRIGGER_DIR`). Files are loaded in sorted alphabetical
order, enabling modular configuration.

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MAITRE_D_TRIGGER_DIR` | `config/triggers.d` | Directory containing trigger YAML files |
| `MAITRE_D_DATA_DIR` | `data` | Directory for persistent trigger state |
| `MAITRE_D_QUEUE_ADDR` | `http://localhost:8080` | Target queue system address (future) |
| `MAITRE_D_WEB_PORT` | `9090` | Port for the web dashboard |
| `MAITRE_D_API_PORT` | `9091` | Port for the webhook API |
| `MAITRE_D_WEBHOOK_DIR` | `config/webhook-endpoints.d` | Directory containing webhook endpoint YAML files |

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
configurable via `MAITRE_D_API_PORT`). TLS termination is expected to be
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
| `--trigger-dir` | Directory containing trigger YAML files (overrides `MAITRE_D_TRIGGER_DIR`) |
| `--data-dir` | Directory for persistent trigger state (overrides `MAITRE_D_DATA_DIR`) |
| `--web-port` | Port for the web dashboard (overrides `MAITRE_D_WEB_PORT`) |
| `--api-port` | Port for the webhook API (overrides `MAITRE_D_API_PORT`) |
| `--webhook-dir` | Directory containing webhook endpoint YAML files (overrides `MAITRE_D_WEBHOOK_DIR`) |
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
