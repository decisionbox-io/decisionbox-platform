# Configuration Reference

> **Version**: 0.4.0

All DecisionBox services are configured via environment variables. This page lists every variable, its default, and which service uses it.

## Agent

The agent (`decisionbox-agent`) is a standalone binary that runs discovery. It reads project configuration from MongoDB but needs environment variables for infrastructure access.

### Required

| Variable | Default | Description |
|----------|---------|-------------|
| `MONGODB_URI` | *(required)* | MongoDB connection string. Examples: `mongodb://localhost:27017`, `mongodb+srv://user:pass@cluster.mongodb.net` |
| `MONGODB_DB` | `decisionbox` | MongoDB database name. Must match the API's database. |

### Secret Provider

The agent reads LLM API keys and warehouse credentials from a secret provider. These are configured per-project via the dashboard.

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRET_PROVIDER` | `mongodb` | Which secret provider to use. Options: `mongodb`, `gcp`, `aws`, `azure` |
| `SECRET_NAMESPACE` | `decisionbox` | Namespace prefix for all secrets. Prevents conflicts in shared cloud accounts. |
| `SECRET_ENCRYPTION_KEY` | *(empty)* | Base64-encoded 32-byte AES key for MongoDB secret provider. Generate with: `openssl rand -base64 32`. If empty, secrets are stored in plaintext (with warning). |
| `SECRET_GCP_PROJECT_ID` | *(empty)* | GCP project ID. Only required when `SECRET_PROVIDER=gcp`. |
| `SECRET_AWS_REGION` | `us-east-1` | AWS region. Only used when `SECRET_PROVIDER=aws`. |
| `SECRET_AZURE_VAULT_URL` | *(empty)* | Azure Key Vault URL (e.g., `https://my-vault.vault.azure.net/`). Only required when `SECRET_PROVIDER=azure`. |

### LLM Behavior

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_MAX_RETRIES` | `3` | Number of retries on LLM API errors (rate limits, timeouts). Set to `0` for no retries. |
| `LLM_TIMEOUT` | `300s` | Timeout per LLM API call. Go duration format: `30s`, `2m`, `5m`. Increased from 120s because large prompts on Opus-class models need more time. |
| `LLM_REQUEST_DELAY_MS` | `1000` | Delay between consecutive LLM calls in milliseconds. Helps with rate limiting and cost control. Set to `0` for no delay. |

### Vector Search (Qdrant)

The agent uses Qdrant to store and index embeddings during the discovery process.

| Variable | Default | Description |
|----------|---------|-------------|
| `QDRANT_URL` | *(empty)* | Qdrant gRPC endpoint (e.g., `qdrant:6334`). If empty, vector indexing is disabled. |
| `QDRANT_API_KEY` | *(empty)* | Optional API key for authenticated Qdrant instances. |

### Operational

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVICE_NAME` | `decisionbox-agent` | Service name that appears in log output. |
| `ENV` | `dev` | Environment. `dev` = human-readable console logs. `prod` or `production` = structured JSON logs. |
| `LOG_LEVEL` | `info` | Log verbosity. Options: `debug`, `info`, `warn`, `error`. |

### Agent CLI Flags

The agent also accepts command-line flags (typically set by the API when spawning):

| Flag | Required | Default | Description |
|------|----------|---------|-------------|
| `--project-id` | Yes | — | Project ID to run discovery for. |
| `--run-id` | No | — | Discovery run ID for live status updates. Set by the API. |
| `--areas` | No | *(all)* | Comma-separated analysis areas to run. Empty = all areas. Example: `--areas churn,monetization` |
| `--max-steps` | No | `100` | Maximum exploration steps. More steps = more comprehensive but slower and more expensive. |
| `--estimate` | No | `false` | Estimate cost only (no actual discovery). Outputs JSON to stdout. |
| `--skip-cache` | No | `false` | Force re-discovery of warehouse schemas (ignore cache). |
| `--enable-debug-logs` | No | `true` | Write detailed debug logs to MongoDB (TTL: 30 days). |
| `--test` | No | `false` | Test mode — limits analysis for faster runs. |

---

## API

The API (`decisionbox-api`) is the REST server that manages projects, discoveries, and spawns agents.

### Required

| Variable | Default | Description |
|----------|---------|-------------|
| `MONGODB_URI` | *(required)* | MongoDB connection string. Must be the same database as the agent. |
| `MONGODB_DB` | `decisionbox` | MongoDB database name. |
| `PORT` | `8080` | HTTP listen port. |

### Secret Provider

Same variables as the agent — the API reads secrets to display masked values in the dashboard.

| Variable | Default | Description |
|----------|---------|-------------|
| `SECRET_PROVIDER` | `mongodb` | Same as agent. Must match. |
| `SECRET_NAMESPACE` | `decisionbox` | Same as agent. Must match. |
| `SECRET_ENCRYPTION_KEY` | *(empty)* | Same as agent. Must match. |
| `SECRET_GCP_PROJECT_ID` | *(empty)* | Same as agent. |
| `SECRET_AWS_REGION` | `us-east-1` | Same as agent. |
| `SECRET_AZURE_VAULT_URL` | *(empty)* | Same as agent. |

### Vector Search (Qdrant)

The API uses Qdrant to perform semantic searches and retrieval of indexed data.

| Variable | Default | Description |
|----------|---------|-------------|
| `QDRANT_URL` | *(empty)* | Qdrant gRPC endpoint (e.g., `qdrant:6334`). If empty, vector search is disabled. |
| `QDRANT_API_KEY` | *(empty)* | Optional API key. |

### Agent Runner

The API spawns the agent for each discovery run. Two modes:

| Variable | Default | Description |
|----------|---------|-------------|
| `RUNNER_MODE` | `subprocess` | How to spawn the agent. `subprocess` = exec.Command (local dev, agent binary must be in PATH). `kubernetes` = create a K8s Job per discovery (production). |

**Subprocess mode** — No additional configuration. The agent binary (`decisionbox-agent`) must be in the system PATH.

**Kubernetes mode** — Additional configuration:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_IMAGE` | `ghcr.io/decisionbox-io/decisionbox-agent:latest` | Docker image for the agent container. |
| `AGENT_NAMESPACE` | `default` | Kubernetes namespace for agent Jobs. |
| `AGENT_SERVICE_ACCOUNT` | `""` | Kubernetes service account for agent Jobs. Set to the agent SA with Workload Identity for GCP Secret Manager / BigQuery access. |
| `AGENT_CPU_REQUEST` | `250m` | CPU request for agent containers (K8s resource quantity). |
| `AGENT_CPU_LIMIT` | `2` | CPU limit for agent containers. |
| `AGENT_MEMORY_REQUEST` | `256Mi` | Memory request for agent containers. |
| `AGENT_MEMORY_LIMIT` | `1Gi` | Memory limit for agent containers. |
| `AGENT_JOB_TIMEOUT_HOURS` | `6` | Maximum time (hours) to watch a K8s Job before giving up. Increase for very large datasets. |

### Operational

| Variable | Default | Description |
|----------|---------|-------------|
| `SERVICE_NAME` | `decisionbox-api` | Service name in logs. |
| `ENV` | `dev` | Environment (`dev` or `prod`). |
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. |

---

## Dashboard

The dashboard (`decisionbox-dashboard`) is a Next.js application that proxies API requests via middleware.

| Variable | Default | Description |
|----------|---------|-------------|
| `API_URL` | `http://localhost:8080` | Backend API URL. **Server-side only** — not exposed to the browser. In Docker: `http://api:8080`. In K8s: `http://decisionbox-api:8080`. |
| `PORT` | `3000` | Dashboard listen port. |
| `HOSTNAME` | `0.0.0.0` | Bind address. `0.0.0.0` = all interfaces. `127.0.0.1` = localhost only. |

**Important:** `API_URL` is a runtime variable read by Next.js middleware on each request. It is NOT baked at build time. This means a single Docker image works across all environments — just change the environment variable.

---

## Docker Compose

The `docker-compose.yml` includes all variables with documentation. Here's the minimal configuration:

```yaml
services:
  mongodb:
    image: mongo:7.0
    ports: ["27017:27017"]
    volumes: [mongodb_data:/data/db]

  api:
    build: { context: ., dockerfile: services/api/Dockerfile }
    ports: ["8080:8080"]
    environment:
      - MONGODB_URI=mongodb://mongodb:27017
      - MONGODB_DB=decisionbox
      - SECRET_PROVIDER=mongodb
      - SECRET_ENCRYPTION_KEY=${SECRET_ENCRYPTION_KEY:-}
      - RUNNER_MODE=subprocess
    depends_on:
      mongodb: { condition: service_healthy }

  dashboard:
    build: { context: ui/dashboard, dockerfile: Dockerfile }
    ports: ["3000:3000"]
    environment:
      - API_URL=http://api:8080
    depends_on: [api]

volumes:
  mongodb_data:
```

### Generating an Encryption Key

For the MongoDB secret provider, generate a 32-byte encryption key:

```bash
# Generate key
openssl rand -base64 32

# Set in docker-compose or .env file
export SECRET_ENCRYPTION_KEY=$(openssl rand -base64 32)
docker compose up -d
```

### File-Based Secrets (Kubernetes)

Environment variables support a `file://` prefix for Kubernetes secret mounts:

```yaml
# In K8s, mount secrets as files and reference them:
SECRET_ENCRYPTION_KEY=file:///var/run/secrets/encryption-key
```

This reads the file contents instead of using the env var value directly.

---

## Precedence

1. **Environment variables** — Highest priority. Override everything.
2. **Defaults in code** — Used when env var is not set.
3. **Project configuration** (MongoDB) — Per-project settings (warehouse, LLM, schedule) are stored in MongoDB and configured via the dashboard.

## Next Steps

- [CLI Reference](cli.md) — Agent command-line flags
- [API Reference](api.md) — REST endpoints
- [Docker Deployment](../deployment/docker.md) — Full deployment guide
