# CLI Reference

> **Version**: 0.4.0

The DecisionBox agent (`decisionbox-agent`) is a standalone Go binary. It's typically spawned by the API, but can be run directly for testing and debugging.

## Usage

```bash
decisionbox-agent [flags]
```

## Flags

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--project-id` | Yes | — | MongoDB ObjectID of the project to analyze. |
| `--run-id` | No | — | Discovery run ID for live status updates. When set, the agent writes progress to the `discovery_runs` collection. If omitted, no live status is written. |
| `--areas` | No | *(all areas)* | Comma-separated list of analysis area IDs to run. If omitted, all areas from the domain pack are run. |
| `--max-steps` | No | `100` | Maximum number of exploration steps. Each step is one LLM call + one SQL query. More steps = more comprehensive but slower and more expensive. |
| `--estimate` | No | `false` | Estimate cost only. Discovers schemas, calculates token estimates, runs dry-run queries, and outputs a JSON cost estimate to stdout. Does not run actual discovery. |
| `--skip-cache` | No | `false` | Force re-discovery of warehouse table schemas, ignoring any cached schemas. |
| `--enable-debug-logs` | No | `true` | Write detailed debug logs to the `discovery_debug_logs` MongoDB collection (TTL: 30 days). Includes full LLM requests/responses. |
| `--test` | No | `false` | Test mode. Limits analysis to one area for faster iteration. |
| `--include-log` | No | `false` | Include the full exploration log in discovery result output. |

## Examples

### Run a Full Discovery

```bash
MONGODB_URI=mongodb://localhost:27017 \
MONGODB_DB=decisionbox \
SECRET_PROVIDER=mongodb \
  decisionbox-agent --project-id=507f1f77bcf86cd799439011
```

### Run Selective Areas

```bash
decisionbox-agent --project-id=507f1f77bcf86cd799439011 --areas=churn,monetization --max-steps=50
```

### Estimate Cost

```bash
decisionbox-agent --project-id=507f1f77bcf86cd799439011 --estimate
```

Outputs JSON to stdout:

```json
{
  "llm": {"provider": "claude", "model": "claude-sonnet-4", "estimated_input_tokens": 250000, "cost_usd": 0.825},
  "warehouse": {"provider": "bigquery", "estimated_queries": 100, "cost_usd": 0.0375},
  "total_cost_usd": 0.8625
}
```

### Run with the API (Typical Flow)

The API spawns the agent automatically. You don't need to run it manually unless debugging:

```bash
# Using make
make agent-run PROJECT_ID=507f1f77bcf86cd799439011

# Using API
curl -X POST http://localhost:8080/api/v1/projects/507f1f77bcf86cd799439011/discover
```

## Environment Variables

The agent requires environment variables for infrastructure access. See [Configuration Reference](configuration.md) for the full list.

Minimum required:

```bash
MONGODB_URI=mongodb://localhost:27017
MONGODB_DB=decisionbox
SECRET_PROVIDER=mongodb
```

## Exit Codes

| Code | Meaning |
|------|---------|
| `0` | Discovery completed successfully |
| `1` | Fatal error (invalid config, authentication failure, all areas failed) |

When the agent exits with code 1, the API's subprocess runner captures the error from stderr and updates the run status to "failed" with the error message.

## Logs

The agent writes structured logs to **stderr** (not stdout). In `dev` mode, logs are human-readable. In `prod` mode, logs are structured JSON for log aggregation.

```bash
# Dev mode (default)
2026-03-14T10:30:00.000Z  INFO  Starting DecisionBox Agent  {"project_id": "507f...", "max_steps": 100}
2026-03-14T10:30:01.000Z  INFO  Phase 2: Discovering schemas  {"datasets": ["analytics"]}
2026-03-14T10:30:05.000Z  INFO  Exploration step starting  {"step": 1, "max": 100}

# Prod mode (ENV=prod)
{"level":"info","ts":"2026-03-14T10:30:00.000Z","msg":"Starting DecisionBox Agent","service":"decisionbox-agent","project_id":"507f..."}
```

## Next Steps

- [Configuration Reference](configuration.md) — All environment variables
- [API Reference](api.md) — REST endpoints that trigger the agent
