# Telemetry

DecisionBox collects **anonymous usage telemetry** to help us understand how the platform is used, prioritize features, and improve reliability.
Telemetry is **enabled by default** and can be disabled with a single environment variable.

## What We Collect

All data is anonymous.
We never collect PII, query content, table names, credentials, insight content, or any data that flows through your warehouse.

| Signal | Example Value | Why |
|--------|---------------|-----|
| Anonymous install ID | `a1b2c3d4-...` (random UUID, stored in MongoDB) | Count unique deployments |
| Product version | `0.4.0` | Know which versions are in use |
| Go version | `go1.25.0` | Runtime compatibility planning |
| OS / architecture | `linux/amd64` | Platform support decisions |
| Deployment method | `kubernetes`, `docker-compose`, `binary` | Prioritize deployment paths |
| Warehouse provider type | `bigquery`, `snowflake` | Provider prioritization |
| LLM provider type | `claude`, `openai`, `ollama` | LLM support prioritization |
| Domain | `gaming`, `ecommerce` | Domain pack prioritization |
| Discovery duration bucket | `1-5m`, `5-15m` | Performance baseline (never exact durations) |
| Insight/recommendation count bucket | `6-20`, `21-50` | Value validation (never exact counts) |
| Error class | `warehouse_error`, `timeout` | Reliability tracking (never error messages) |
| Feature flags | `vector_search: true`, `auth_enabled: false` | Feature adoption |

### What We Never Collect

- Connection strings, API keys, passwords, or credentials
- Warehouse, database, schema, or table names
- SQL queries or query results
- Insight or recommendation content
- User names, emails, IP addresses, or hostnames
- Project names or descriptions
- Any data from your warehouse

## How to Opt Out

Set **one** of these environment variables to disable telemetry completely:

```bash
# Option 1: DecisionBox-specific
TELEMETRY_ENABLED=false

# Option 2: DO_NOT_TRACK standard (https://consoledonottrack.com/)
DO_NOT_TRACK=1
```

When disabled, no data is collected and no network requests are made.

### Docker Compose

```yaml
services:
  api:
    environment:
      - TELEMETRY_ENABLED=false
  agent:
    environment:
      - TELEMETRY_ENABLED=false
```

### Kubernetes (Helm)

```yaml
# values.yaml
env:
  TELEMETRY_ENABLED: "false"
```

### Setup Wizard

The interactive setup wizard (`terraform/setup.sh`) asks about telemetry during the prerequisites step.
You can also pass it as a flag:

```bash
TELEMETRY_ENABLED=false ./setup.sh
```

## Events

These are the exact events sent by DecisionBox:

### `server_started`

Sent once when the API server starts.

```json
{
  "name": "server_started",
  "properties": {
    "deployment_method": "kubernetes",
    "auth_enabled": false,
    "vector_search": true
  }
}
```

### `server_stopped`

Sent when the API server shuts down gracefully.
No properties.

### `project_created`

Sent when a new project is created.

```json
{
  "name": "project_created",
  "properties": {
    "warehouse_provider": "bigquery",
    "llm_provider": "claude",
    "domain": "gaming"
  }
}
```

### `discovery_completed`

Sent when a discovery run finishes successfully.
Counts are bucketed (never exact).

```json
{
  "name": "discovery_completed",
  "properties": {
    "warehouse_provider": "bigquery",
    "llm_provider": "claude",
    "domain": "gaming",
    "domain_pack": "match-3",
    "duration_bucket": "5-15m",
    "insights_bucket": "6-20",
    "recs_bucket": "1-5",
    "queries_bucket": "21-50"
  }
}
```

### `discovery_failed`

Sent when a discovery run fails.
Only the error class is sent, never the actual error message.

```json
{
  "name": "discovery_failed",
  "properties": {
    "warehouse_provider": "snowflake",
    "llm_provider": "openai",
    "domain": "ecommerce",
    "error_class": "warehouse_error"
  }
}
```

## Implementation

The telemetry implementation is fully open source:

- **Client library**: [`libs/go-common/telemetry/`](libs/go-common/telemetry/) -- event collection, batching, HTTP sender
- **Telemetry endpoint**: [`decisionbox-telemetry-worker`](https://github.com/decisionbox-io/decisionbox-telemetry-worker) -- Cloudflare Worker + D1
- **API integration**: [`services/api/apiserver/apiserver.go`](services/api/apiserver/apiserver.go) -- init, server events
- **Agent integration**: [`services/agent/agentserver/agentserver.go`](services/agent/agentserver/agentserver.go) -- discovery events

Events are batched in memory and sent every 5 minutes (or on shutdown).
The flush interval is configurable via `TELEMETRY_FLUSH_INTERVAL` (default: `5m`, Go duration format).
Network errors are silently ignored -- telemetry never affects application behavior.

## Install ID

A random UUID is generated on first startup and stored in the `telemetry_settings` MongoDB collection.
This ID is used to count unique deployments.
It is not linked to any user identity and cannot be used to identify you.

## Questions?

Open a [GitHub Discussion](https://github.com/decisionbox-io/decisionbox-platform/discussions) or file an [issue](https://github.com/decisionbox-io/decisionbox-platform/issues).
