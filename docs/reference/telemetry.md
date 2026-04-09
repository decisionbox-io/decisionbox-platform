# Telemetry

> **Version**: 0.4.0

DecisionBox collects anonymous usage telemetry to help us understand how the platform is used, prioritize features, and improve reliability.
Telemetry is enabled by default and can be disabled with a single environment variable.

## Opt Out

Set one of these environment variables to disable telemetry completely:

| Variable | Value | Standard |
|----------|-------|----------|
| `TELEMETRY_ENABLED` | `false` | DecisionBox-specific |
| `DO_NOT_TRACK` | `1` | [Console Do Not Track](https://consoledonottrack.com/) |

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
env:
  TELEMETRY_ENABLED: "false"
```

## What Is Collected

All data is anonymous.
No PII, query content, table names, credentials, or warehouse data is ever collected.

| Signal | Example | Purpose |
|--------|---------|---------|
| Install ID | Random UUID | Count unique deployments |
| Version | `0.4.0` | Version distribution |
| Go version | `go1.25.0` | Runtime compatibility |
| OS / architecture | `linux/amd64` | Platform support |
| Deployment method | `kubernetes` | Deployment prioritization |
| Warehouse provider | `bigquery` | Provider prioritization |
| LLM provider | `claude` | LLM support planning |
| Domain | `gaming` | Domain pack prioritization |
| Duration bucket | `5-15m` | Performance baseline |
| Count buckets | `6-20` | Usage patterns |
| Error class | `warehouse_error` | Reliability tracking |
| Feature flags | `vector_search: true` | Feature adoption |

## Events

Five event types are sent:

| Event | When | Key Properties |
|-------|------|----------------|
| `server_started` | API server starts | deployment method, feature flags |
| `server_stopped` | API server stops | (none) |
| `project_created` | New project created | warehouse/LLM provider, domain |
| `discovery_completed` | Discovery finishes | provider types, duration/count buckets |
| `discovery_failed` | Discovery fails | provider types, error class |

## Privacy Guarantees

- **No PII**: No IP addresses, hostnames, user names, or emails
- **No content**: No SQL queries, insights, recommendations, or warehouse data
- **No identifiers**: No project names, table names, or database names
- **Bucketed counts**: Exact counts are never sent -- only coarse buckets (`1-5`, `6-20`, etc.)
- **Bucketed durations**: Exact durations are never sent -- only buckets (`<1m`, `1-5m`, etc.)
- **Error classes only**: Error messages are classified (`warehouse_error`, `timeout`) -- the message itself is never sent
- **Silent failures**: Telemetry network errors are ignored -- they never affect application behavior

## Install ID

A random UUID is generated on first startup and stored in the `telemetry_settings` MongoDB collection.
It persists across container restarts so we can count unique deployments.
It is not linked to any user identity and cannot be used to identify you.

## Implementation

The telemetry code is fully open source:

- Client library: `libs/go-common/telemetry/`
- Endpoint: [decisionbox-telemetry-worker](https://github.com/decisionbox-io/decisionbox-telemetry-worker) (Cloudflare Worker + D1)

Events are batched hourly and sent via HTTPS.
The endpoint URL is configurable via `TELEMETRY_ENDPOINT` (default: `https://telemetry.decisionbox.io/v1/events`).

## See Also

- [TELEMETRY.md](https://github.com/decisionbox-io/decisionbox-platform/blob/main/TELEMETRY.md) -- Full telemetry documentation in the repository
- [Configuration Reference](configuration.md) -- All environment variables
