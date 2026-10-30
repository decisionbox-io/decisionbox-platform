# CLI Reference

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
| `--min-steps` | No | `0` | Minimum exploration steps before the agent accepts a completion signal. If the LLM returns `{"done": true}` before this count, the signal is rejected, a `complete_rejected` step is recorded, and exploration continues with a nudge. `0` (default) disables the floor. Use on reasoning models (Qwen3, DeepSeek-R1, GPT-OSS) that tend to terminate exploration too early — a reasonable starting value is 50–70% of `--max-steps`. A run that can query a cube-shaped datasource honours this floor too, and then keeps exploring past it while steps are still turning up something new — so the flag is a minimum there as everywhere, never a maximum. `--max-steps` still caps it. |
| `--resume` | No | `false` | Resume the run named by `--run-id` from its last exploration checkpoint instead of starting fresh. The agent replays the steps already executed — no warehouse re-queries and no exploration LLM calls for them — and continues from the next one; a run whose exploration already finished goes straight to analysis. Requires `--run-id`, and fails with a clear error when that run has no checkpoint left (it expired, or the run died before its first step). Normally set by the API's `POST /api/v1/runs/{runId}/resume` rather than by hand. |
| `--skip-cache` | No | `false` | Force re-discovery of warehouse table schemas, ignoring any cached schemas. |
| `--enable-debug-logs` | No | `true` | Write detailed debug logs to the `discovery_debug_logs` MongoDB collection (TTL: 30 days). Includes full LLM requests/responses. |
| `--test` | No | `false` | Test mode. Limits analysis to one area for faster iteration. |
| `--include-log` | No | `false` | Include the full exploration log in discovery result output. |
| `--test-connection` | No | *(empty)* | Test a provider connection and exit. One of `warehouse`, `llm`, `embedding`, or `blurb-llm`. |
| `--mode` | No | *(empty)* | Alternate run mode: `index-schema` (build the project's schema retrieval index and exit), `validate-doc` (validate one insight or recommendation for the job named by `--job-id`), or `validate-sql` (compile-check a batch of SQL statements against the project's warehouse for the job named by `--job-id`). Empty runs discovery. |
| `--job-id` | No | *(empty)* | Job `_id` when `--mode=validate-doc` (a `validation_jobs` row) or `--mode=validate-sql` (a `sql_validation_jobs` row). Ignored in other modes. |

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
2026-03-14T10:30:01.000Z  INFO  Discovering schemas  {"datasets": ["analytics"]}
2026-03-14T10:30:05.000Z  INFO  Exploration step starting  {"step": 1, "max": 100}

# Prod mode (ENV=prod)
{"level":"info","ts":"2026-03-14T10:30:00.000Z","msg":"Starting DecisionBox Agent","service":"decisionbox-agent","project_id":"507f..."}
```

## API subcommands

The API binary (`decisionbox-api`) also carries one-shot maintenance
subcommands.
Invoking one runs it and exits without starting the HTTP server, so they are
safe to run as a Kubernetes Job against the same image and environment the API
uses.

| Subcommand | What it does |
|------------|--------------|
| `backfill-embeddings` | Denormalizes insights and recommendations into their standalone collections and embeds them into Qdrant. Needed after enabling an embedding provider on a project with existing discoveries. |
| `backfill-ledger-graph` | Recovers the Discovery Ledger provenance that is derivable for runs that completed before it was recorded. No re-discovery. |

### `backfill-ledger-graph`

Each discovery run records where its findings came from — the insight behind
each one, a sighting per run that saw it, why a status moved, and what closed a
thread.
A project with earlier history has none of that, and re-running discovery would
not recover it: a new run produces today's findings, not last year's.

This recovers what is derivable and reports what is not.

```bash
# Every project, reporting what would change without writing
decisionbox-api backfill-ledger-graph --dry-run

# One project
decisionbox-api backfill-ledger-graph --project-id 507f1f77bcf86cd799439011
```

| Flag | Default | Description |
|------|---------|-------------|
| `--project-id` | *(all projects with ledger findings)* | Backfill one project. |
| `--dry-run` | `false` | Report the counts and write nothing. |
| `--batch-size` | `200` | Maximum writes per bulk operation. |
| `--close-window` | `6h` | How close a run must be to a closed thread's last write to be called the run that closed it. A window containing two runs leaves the closing run unset. |

What it derives, and what it deliberately does not:

- **A finding's source insight** is linked when the insight's area and name
  produce the finding's stored key — an exact match, not a similarity score.
  A finding that absorbed a reworded insight through the semantic-dedup path
  does not match; it is counted as unlinkable and left alone.
  No vector search: a wrong provenance link sends a reader to the SQL for a
  different finding and looks correct, while a missing one shows as not linked.
- **One sighting** per finding, from its first-seen date and its source
  discovery. The intermediate runs behind a recurrence count were never dated
  and are not invented; the count itself stays as it is.
- **One status entry** for a finding whose status has no history, carrying no
  reason, because nothing stored says why it moved.
- **A closed thread's close date** is its last write. The run that closed it is
  stamped only when exactly one run falls inside `--close-window`. The findings
  that answered a thread are never guessed.

It is idempotent: every write is conditional on the field being absent, so a
re-run changes nothing and an interrupted run can simply be repeated.
`MONGODB_URI` is required; `MONGODB_DB` defaults to `decisionbox`.

## Next Steps

- [Configuration Reference](configuration.md) — All environment variables
- [API Reference](api.md) — REST endpoints that trigger the agent
