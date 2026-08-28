# Configuration Reference

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
| `LLM_TIMEOUT` | `300s` | HTTP timeout per LLM API call. Go duration format: `30s`, `2m`, `5m`. Read by the agent at startup and threaded through to every provider as `cfg["timeout_seconds"]`. Per-project `timeout_seconds` in the LLM config (dashboard) overrides this when set. Invalid or zero values fall back to the `300s` default. The API process reads the same env var but with no default — when unset, each provider keeps its own hard-coded fallback (60s for Claude direct API, 5m for OpenAI/Bedrock/Vertex/Azure Foundry, 15m for Ollama (reasoning-on local generations want a longer window)); see the API Configuration section below. |
| `LLM_REQUEST_DELAY_MS` | `1000` | Delay between consecutive LLM calls in milliseconds. Helps with rate limiting and cost control. Set to `0` for no delay. |
| `RECOMMENDATION_PARSE_MAX_RETRIES` | `1` | How many corrective re-prompts the recommendation phase issues when a response yields zero parseable recommendations. The retry fires only on a genuine parse failure (never on a partial success or a legitimately empty result), so it costs nothing on the happy path. Set to `0` to disable. |
| `EXPLORATION_MAX_OUTPUT_TOKENS` | `4096` | Per-step output-token ceiling for the exploration LLM call (the effective budget is the smaller of this and the model's catalogued output cap). The default is conservative so the reservation can't overflow a small-context deployment (e.g. an 8K/4K Ollama `num_ctx`). Raise it when running reasoning models on large-context backends so a long thinking block doesn't truncate the action to empty. |
| `ANALYSIS_MIN_OUTPUT_TOKENS` | `8192` | Floor on the output-token budget the analysis and recommendation phases request. Each phase caps `max_tokens` at `context_window − input − reserved − margin` so `input + output` never exceeds the model window (see the note below); this floor stops a very large input from squeezing the output to zero. When even the floor won't fit, the request may still be rejected and the adaptive context-overflow retry recomputes a fitting `max_tokens` from the model's own error. Set to `0`/blank to keep the default. |

### Discovery Run Budget

| Variable | Default | Description |
|----------|---------|-------------|
| `DISCOVERY_MAX_DURATION` | `24h` | In-agent ctx cap on a single discovery run. Go duration format: `2h`, `24h`, `168h`. Acts as a runaway-loop safety net — per-step budgets (warehouse `QueryTimeout`, `LLM_TIMEOUT` + `LLM_RETRY_*`, per-table schema timeout) are what keep stuck operations responsive within a run. Set to `0` to disable the in-agent cap entirely for installs that prefer to rely solely on per-step budgets (typical for very large warehouses with multi-hour SQL scans). Invalid or negative values log a warning and fall back to `24h`. The tail-end persistence step (Mongo writes, embed/index, status update) always runs under its own 10-minute budget regardless of this setting, so a completed run is never lost to a deadline. **Must coexist with `AGENT_JOB_TIMEOUT_HOURS` on the API side:** the API's K8s Job `ActiveDeadlineSeconds` and the subprocess watcher are driven by `AGENT_JOB_TIMEOUT_HOURS`, so the agent is killed outright at that wall-clock cap regardless of `DISCOVERY_MAX_DURATION`. Keep `DISCOVERY_MAX_DURATION` < `AGENT_JOB_TIMEOUT_HOURS` so the in-agent cap fires first and the agent saves partial results gracefully. For multi-hour SQL, raise both consistently (e.g. `AGENT_JOB_TIMEOUT_HOURS=25` + `DISCOVERY_MAX_DURATION=24h`). |

### Validation

The LLM-native verifier + refuter run in Phase 4.5 (insights) and Phase 5.5 (recommendations) of every discovery. See [Insight validation](../architecture/insight-validation.md) for the architectural overview.

| Variable | Default | Description |
|----------|---------|-------------|
| `VALIDATION_REFUTER_ENABLED` | `true` | When `false`, only the verifier runs. The refuter side carries no weight in Combine() and every doc is stamped `refuter_disabled: true`. Useful when refuter telemetry shows ≤5% rejection rate and the cost isn't pulling its weight. |
| `VALIDATION_MAX_INSIGHTS_PER_RUN` | `30` | Run-level cap on validated insights. Insights are ordered by `affected_count` descending; surplus get `combined = "skipped_budget_cap"`. |
| `VALIDATION_MAX_RECOMMENDATIONS_PER_RUN` | `15` | Run-level cap on validated recommendations. |
| `VALIDATION_VERIFIER_MAX_ROUNDS` | `8` | Max LLM rounds per verifier run before forced-final. |
| `VALIDATION_VERIFIER_TOKEN_CAP` | `30000` | Soft cap on cumulative tokens (input + output) per verifier run. When exceeded, the loop bails to forced-final. |
| `VALIDATION_VERIFIER_MAX_OUTPUT` | `4000` | Per-call max output tokens for the verifier. |
| `VALIDATION_REFUTER_MAX_ROUNDS` | `6` | Same as the verifier knob, refuter side. |
| `VALIDATION_REFUTER_TOKEN_CAP` | `20000` | Soft token cap for the refuter. |
| `VALIDATION_REFUTER_MAX_OUTPUT` | `3000` | Per-call max output tokens for the refuter. |
| `VALIDATION_BUNDLE_SAMPLE_ROWS` | `50` | How many rows per source step the bundle samples for `read_step_rows`. |
| `VALIDATION_BUNDLE_CELL_CHAR_CAP` | `200` | Per-cell character cap on row values in the rendered bundle. Strings over the cap are truncated with an ellipsis. |
| `VALIDATION_REC_STEPS_TOKEN_BUDGET` | `12000` | Token budget for the union of source steps a recommendation bundle includes. Over-budget steps are omitted and `source_steps_truncated: true` is surfaced in the prompt. |
| `VALIDATION_ESTIMATE_TOKEN_RATIO` | `3.5` | Characters-to-token ratio used for the in-loop prompt-size estimate. Lower → more conservative budgeting. |
| `VALIDATION_MAX_READ_STEP_ROWS` | `200` | Per-call cap on the row count the `read_step_rows` tool returns. The agent may still ask for more; we silently clamp and the result carries `truncated: true` so the agent knows further rows are available. |
| `VALIDATION_NUMERIC_TOLERANCE` | `0.20` | Relative tolerance (±20% by default) for comparing a claim's quantitative figure against row evidence. Prevents rounding-noise rejections — e.g. a "27% spike" claim with evidence of 26.5% stays `supported`. Only applies to magnitude/figure components; ranking and superlative claims are exact-match. |
| `VALIDATION_MIN_SAMPLE_SIZE` | `30` | Minimum row population the refuter must observe before using a row as counter-evidence for a market-wide superlative claim. Below this, a contradicting outlier is dismissed (apples-to-apples — small-sample contradictions don't disprove the headline). |

### Batch SQL Validation

The `--mode=validate-sql` run mode compile-checks a batch of SQL statements against a project's warehouse via the warehouse's native compile-only path (see the [CLI reference](cli.md)).

| Variable | Default | Description |
|----------|---------|-------------|
| `SQL_VALIDATION_MAX_STATEMENTS` | `500` | Maximum statements a single `validate-sql` job may carry. A batch over the cap fails the job (compile round-trips against the warehouse stay bounded). Set to `0` to disable the cap. |

### Vector Search (Qdrant)

The agent uses Qdrant to store and index embeddings during the discovery process.

| Variable | Default | Description |
|----------|---------|-------------|
| `QDRANT_URL` | *(empty)* | Qdrant gRPC endpoint (e.g., `qdrant:6334`). If empty, vector indexing is disabled. |
| `QDRANT_API_KEY` | *(empty)* | Optional API key for authenticated Qdrant instances. |

### Telemetry

| Variable | Default | Description |
|----------|---------|-------------|
| `TELEMETRY_ENABLED` | `true` | Enable anonymous usage telemetry. Set to `false` to disable. See [Telemetry](telemetry.md) for details. |
| `DO_NOT_TRACK` | *(empty)* | Set to `1` to disable telemetry. Follows the [Console Do Not Track](https://consoledonottrack.com/) standard. |
| `TELEMETRY_ENDPOINT` | `https://telemetry.decisionbox.io/v1/events` | Telemetry collection endpoint. Override for self-hosted collection. |
| `TELEMETRY_FLUSH_INTERVAL` | `5m` | How often to send batched telemetry events. Go duration format: `30s`, `5m`, `1h`. |

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
| `--mode` | No | *(empty)* | Alternate run mode: `index-schema`, `validate-doc`, or `validate-sql` (batch SQL compile-check). Empty runs discovery (the default). |
| `--project-id` | Yes | — | Project ID to run discovery for. |
| `--run-id` | No | — | Discovery run ID for live status updates. Set by the API. |
| `--areas` | No | *(all)* | Comma-separated analysis areas to run. Empty = all areas. Example: `--areas churn,monetization` |
| `--max-steps` | No | `100` | Maximum exploration steps. More steps = more comprehensive but slower and more expensive. |
| `--min-steps` | No | `0` | Minimum exploration steps before the agent accepts a `done` signal from the LLM. Early `done` signals are rejected (recorded as `complete_rejected`) and exploration continues. `0` disables the floor. Use on reasoning models (Qwen3, DeepSeek-R1, GPT-OSS) that terminate too early. |
| `--estimate` | No | `false` | Estimate cost only (no actual discovery). Outputs JSON to stdout. |
| `--skip-cache` | No | `false` | Force re-discovery of warehouse schemas (ignore cache). |
| `--enable-debug-logs` | No | `true` | Write detailed debug logs to MongoDB (TTL: 30 days). |
| `--test` | No | `false` | Test mode — limits analysis for faster runs. |
| `--test-connection` | No | *(empty)* | Test a provider connection and exit. One of `warehouse`, `llm`, `embedding`, `blurb-llm`. |
| `--job-id` | No | *(empty)* | Job `_id` when `--mode=validate-doc` (`validation_jobs`) or `--mode=validate-sql` (`sql_validation_jobs`). Ignored in other modes. |

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

### LLM Behavior

The API talks to LLMs for `/ask`. Per-project LLM credentials and `timeout_seconds` are read from the project's LLM config (set in the dashboard); these env vars are deployment-wide defaults.

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_TIMEOUT` | *(provider-specific)* | HTTP timeout per LLM API call, applied to every registered provider. Go duration format: `30s`, `2m`, `15m`. Per-project `timeout_seconds` overrides this when set. When unset, providers use their hard-coded default (60s for Claude direct API, 5m for OpenAI/Bedrock/Vertex/Azure Foundry, 15m for Ollama (reasoning-on local generations want a longer window)). Raise this when long-form generations (e.g. executive summaries on Opus-class models) exceed 5 minutes. Same env var the agent reads — set it once on both containers. |

**No env vars are needed for `/ask` context budgeting.** The handler reads each model's published context window from its catalog entry (`MaxInputTokens`) and sizes the prompt against it automatically. Models with no declared window fall back to a conservative 32K. See [Ask: Token-Aware Context Budgeting](../concepts/ask.md) for the full algorithm and the typed error codes the dashboard branches on.

### Agent Runner

The API spawns the agent for each discovery run. Three modes:

| Variable | Default | Description |
|----------|---------|-------------|
| `RUNNER_MODE` | `subprocess` | How to spawn the agent. `subprocess` = exec.Command (local dev, agent binary must be in PATH). `docker` = spawn the agent as its own container from `AGENT_IMAGE` via the Docker engine (single-host / docker-compose). `kubernetes` = create a K8s Job per discovery (production). |

**Subprocess mode** — No additional configuration. The agent binary (`decisionbox-agent`) must be in the system PATH.

**Docker mode** — The API spawns each agent invocation as its own short-lived container from `AGENT_IMAGE`, on a configurable Docker network, and removes it on completion. Use it when the agent image must differ from the API image, or to mirror the production "API spawns the agent from an image" model on a single host without a cluster.

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_IMAGE` | `ghcr.io/decisionbox-io/decisionbox-agent:latest` | Container image spawned per run (shared with kubernetes mode). A public or locally-built image works: if it is not present, the runner attempts to pull it and surfaces a clear error if the pull fails. The pull is sent with **no registry credentials**, so a private-registry image must be pre-pulled (or otherwise made available to the daemon). For AWS/EKS deployments, use the ECR mirror: `<account-id>.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>` — EKS nodes pull from same-account ECR automatically via the node IAM role (no extra credentials needed when the ECR repo is in the same account as the cluster). |
| `AGENT_DOCKER_NETWORK` | `""` | Docker network the agent container joins so it can resolve `mongodb`, `qdrant`, and warehouse hosts by service name on the compose network. Empty = the engine's default network. For docker-compose, set it to the project's default network — `<project>_default`, where `<project>` defaults to the Compose directory name (so `decisionbox-platform_default` for a clone of this repo; confirm with `docker network ls`). |
| `DOCKER_HOST` | *(unset)* | Standard Docker variable selecting the engine endpoint. Unset = the default Unix socket `/var/run/docker.sock`, which must be mounted into the API container. |

The agent container receives the same Mongo / secret-provider / Qdrant / validation configuration the kubernetes runner forwards, plus passthrough (when set) of: AWS credentials (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `AWS_REGION`, `AWS_DEFAULT_REGION`), Azure credentials (`AZURE_TENANT_ID`, `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`), the LLM **behaviour** knobs (`LLM_TIMEOUT`, `LLM_MAX_RETRIES`, `LLM_REQUEST_DELAY_MS`, `LLM_RETRY_BASE_BACKOFF`, `LLM_RETRY_MAX_ATTEMPTS`), `ENV` / `LOG_LEVEL`, and the telemetry opt-out (`TELEMETRY_ENABLED`, `DO_NOT_TRACK`) so an opted-out deployment's agent containers stay opted out. LLM **API keys** are NOT forwarded — they are loaded per-project from the secret provider.

**GCP credentials are not auto-forwarded.** `GOOGLE_APPLICATION_CREDENTIALS` is a file path, and the runner cannot bind-mount that file into the spawned agent container (it knows only the path inside the API container, not the host path), so forwarding it would resolve to a missing file. For `SECRET_PROVIDER=gcp` / BigQuery in docker mode, give the agent credentials another way: the host's GCE metadata server / Workload Identity (reachable from the agent network), or a service-account key baked into or mounted onto a custom `AGENT_IMAGE`.

> **Security:** mounting the Docker socket grants the API root-equivalent access to the host. Docker mode is a local / single-host convenience — use the `kubernetes` runner in production.

**Kubernetes mode** — Additional configuration:

| Variable | Default | Description |
|----------|---------|-------------|
| `AGENT_IMAGE` | `ghcr.io/decisionbox-io/decisionbox-agent:latest` | Docker image for the agent container. For AWS/EKS deployments, use the ECR mirror: `<account-id>.dkr.ecr.us-east-1.amazonaws.com/decisionbox-agent:<tag>`. |
| `AGENT_NAMESPACE` | `default` | Kubernetes namespace for agent Jobs. |
| `AGENT_SERVICE_ACCOUNT` | `""` | Kubernetes service account for agent Jobs. Set to the agent SA with Workload Identity for GCP Secret Manager / BigQuery access. |
| `AGENT_CPU_REQUEST` | `250m` | CPU request for agent containers (K8s resource quantity). |
| `AGENT_CPU_LIMIT` | `2` | CPU limit for agent containers. |
| `AGENT_MEMORY_REQUEST` | `256Mi` | Memory request for agent containers. |
| `AGENT_MEMORY_LIMIT` | `1Gi` | Memory limit for agent containers. |
| `AGENT_JOB_TIMEOUT_HOURS` | `25` | Wall-clock budget for one agent run. Used as the K8s Job's `ActiveDeadlineSeconds` and the Docker runner's per-run wall-clock budget — hard kill at the cap in both — as well as the subprocess watcher timeout. The default is paired with the agent's 24h `DISCOVERY_MAX_DURATION` default so the in-agent cap fires first (with 1h headroom for the agent's 10-minute persistence tail + clock skew) and the agent fails gracefully rather than being killed mid-write. If you change `DISCOVERY_MAX_DURATION` you must keep this value at least 1h above it; a startup `WARN` log fires when they are inconsistent. A non-positive value is normalized back to the default, so the cap is always in effect. |
| `OPENSHIFT_ENABLED` | `false` | Set to `true` on OpenShift/OKD. Omits the fixed `runAsUser` / `runAsGroup` / `fsGroup` (`1000`) from the agent Job pod spec so the `restricted-v2` SCC assigns a UID/GID from the namespace's allocated range — pinning `1000` there is rejected as out-of-range and the pod never schedules (Test Connection / discovery fail with a 504). All other pod hardening (`runAsNonRoot`, drop `ALL` capabilities, read-only root filesystem, `RuntimeDefault` seccomp) is kept. Leave unset/`false` on vanilla Kubernetes — the fixed UID `1000` is retained. Unparseable values fall back to `false`. |

### Telemetry

Same variables as the agent — see the [Agent Telemetry](#telemetry) section above.

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

## Build metadata

Version metadata is stamped into the images at **build time** (not runtime) and surfaced by the API's [`GET /api/v1/system`](api.md#get-apiv1system) endpoint and the dashboard's **System** page.
These are Docker **build args** (`--build-arg` / `docker compose build` args), not environment variables.

| Build arg | Images | Default | Description |
|-----------|--------|---------|-------------|
| `VERSION` | API, Agent, Dashboard | `dev` | Release version. The Go binaries receive it via `-ldflags`; the dashboard maps it to `NEXT_PUBLIC_DASHBOARD_VERSION` (falling back to `package.json` when unset). |
| `COMMIT` | API, Agent | `unknown` | Git commit the binary was built from (`-ldflags`). |
| `BUILD_DATE` | API, Agent, Dashboard | `unknown` | RFC3339 build timestamp. |

A binary built with no `-ldflags` at all — e.g. a plain `go build` — reports the source-tree default (`0.4.0-dev`). Images built via the Dockerfiles always inject `-ldflags`, so an image built without the build args above reports the Dockerfile defaults (`VERSION=dev`, `COMMIT`/`BUILD_DATE=unknown`) rather than `0.4.0-dev`.

`make docker-build` computes these values from the git tag/commit via `.github/scripts/build-metadata.sh` and passes them to each image build, so a built image reports the version it was published as.
To stamp a build directly:

```bash
make docker-build                       # auto-detects version from git
# or pass explicit values:
VERSION=0.10.0 COMMIT=$(git rev-parse --short HEAD) BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  docker compose build
```

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
3. **Project configuration** (MongoDB) — Per-project settings (warehouse, LLM) are stored in MongoDB and configured via the dashboard.

## Analysis Phase Compaction Tunables

Bounded analysis prompts via vector-ranked step selection + per-step
compact digest. Algorithm parameters live in code, not env vars —
edit and redeploy to change. See
[agent-analysis-compaction.md](../architecture/agent-analysis-compaction.md)
for the full design.

| Constant                                                              | Default     | What it controls                                                                                                  |
|-----------------------------------------------------------------------|-------------|-------------------------------------------------------------------------------------------------------------------|
| `models.CompactInlineThreshold`                                       | `20`        | Row-count cap below which the digest stores every row verbatim (`AllRows`). Above the cap, only head + tail.      |
| `models.TopValueCardinality`                                          | `20`        | Distinct-value cap above which a string column emits no `Top` list — guards against PII (user IDs / free text).   |
| `models.HeadTailRowCount`                                             | `5`         | Rows in `HeadRows` and `TailRows` (the boundary samples for results above the inline threshold).                  |
| `discovery.AnalysisAreaTopK`                                          | `24`        | Maximum vector hits per area before exact-match boost + budget trim.                                              |
| `discovery.AnalysisAreaMinScore`                                      | `0.30`      | Cosine-similarity floor; vector hits below it are dropped (recorded as `below_min_score` in telemetry).           |
| `discovery.ExactMatchFloor`                                           | `0.55`      | Score assigned to steps promoted via the keyword exact-match boost — set above the min-score floor.               |
| `discovery.AnalysisQueryResultsBudgetTokens`                          | `200_000`   | Ceiling on the rendered `{{QUERY_RESULTS}}` block, in tokens. At run time this is further capped to `context_window − output − reserved − margin`, so an area's input can never grow past what the model window holds once the output is reserved. Picker drops lowest-scored steps until under the effective budget. |

When to tune:

- **`CompactInlineThreshold`** — Lower (10) if your domain produces
  many 20-50 row aggregates and the head+tail summary loses too much
  detail.
- **`AnalysisAreaTopK`** — Lower if you regularly hit budget trimming;
  higher if the picker is clearly missing relevant steps.
- **`AnalysisAreaMinScore`** — Lower (0.20) for highly-multilingual runs
  where cosine similarity is naturally smaller across languages.
- **`AnalysisQueryResultsBudgetTokens`** — Lower if the surrounding
  prompt grows; raise only on models with substantially-larger context
  windows. It is already auto-capped at run time to the model's window
  minus the reserved output, so on small-window models you rarely need
  to touch it.

### Budgeting output against the model window

The analysis and recommendation phases size their requested `max_tokens`
against the model's context window so `input + output` never overflows it
(the cause of hard "maximum context length" 400s on ~200K-context models).
The window is resolved without depending on the shipped catalog, so it
works for arbitrary customer models: **operator override → self-calibration
→ live auto-detection → catalog → default**.

- **Operator override** — set `max_input_tokens` (and, if the model's
  output cap differs from the default, `max_output_tokens`) in the
  project's LLM config. Highest priority; use it for any model DecisionBox
  cannot detect.
- **Live auto-detection** — LiteLLM (`GET /model/info`), Ollama
  (`GET /api/show` `context_length`), and OpenAI-compatible gateways that
  expose `max_model_len` report their real window; the dashboard prefills
  the two override fields from it on model selection.
- **Self-calibration** — if a request still overflows, the model's error
  states its true window; the agent recomputes a fitting `max_tokens`,
  retries once, and records the window (`llm_model_windows` collection) so
  later runs budget correctly up front.

## Next Steps

- [CLI Reference](cli.md) — Agent command-line flags
- [API Reference](api.md) — REST endpoints
- [Docker Deployment](../deployment/docker.md) — Full deployment guide
