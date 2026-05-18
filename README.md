# maitred

> Periodic trigger engine for autonomous task queues.

maitred schedules and dispatches tasks into any queue system based on
periodic cron or duration-based triggers. It reads trigger definitions
from YAML files, evaluates prompt templates with execution state, and
pushes resulting tasks to a configurable queue provider.

Designed to run alongside [hotelier](https://github.com/skrewz/hotelier)
but works with any system that implements the `TaskQueueProvider` interface.

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
| `prompt` | ✅ | Go template with `.LastRun` variable |
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

## Architecture

```
┌─────────────────────────────────────────────────┐
│                    maitred                       │
│                                                  │
│  config/triggers.d/   pkg/engine/   pkg/state/   │
│  ┌──────────────┐    ┌──────────┐   ┌────────┐  │
│  │ YAML files   │───►│ Engine   │──►│ JSON   │  │
│  │ (parsed at   │    │ (cron    │   │ files  │  │
│  │  startup)    │    │  ticker  │   │ per    │  │
│  └──────────────┘    │  goroutine│  │ trigger│  │
│                      └────┬─────┘   └────────┘  │
│                           │                      │
│                           ▼                      │
│                    ┌──────────────┐              │
│                    │ TaskQueue    │              │
│                    │ Provider     │              │
│                    └──────┬───────┘              │
└───────────────────────────┬─────────────────────┘
                            │
                            ▼
                  ┌──────────────────┐
                  │  Queue System    │
                  │ (hotelier, etc.) │
                  └──────────────────┘
```

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
