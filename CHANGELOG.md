# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Knowledge sources hook (community plumbing)** — New `libs/go-common/sources` package introduces a `Provider` interface, registry, no-op default, and a `FormatPromptSection` helper that renders retrieved chunks as a deterministic `## Project Knowledge` markdown block. Discovery orchestrator injects retrieved source context into exploration (top-K=3), per-area analysis (top-K=5), and recommendation (top-K=8) prompts; `/ask` includes source chunks alongside insights and recommendations with `[s1]` citation labels. Without an enterprise plugin loaded, the no-op provider returns no chunks and behavior is identical to prior releases. `apiserver` and `agentserver` now call `sources.Configure` after MongoDB / Qdrant / secrets are ready so an enterprise plugin can wire its retriever in.
- **Microsoft SQL Server warehouse provider** — Connect to SQL Server 2016+ and Azure SQL Database in read-only mode. Two auth methods: SQL login (username/password) and full connection string. Type normalization for all SQL Server types (TINYINT/SMALLINT/INT/BIGINT → INT64; REAL/FLOAT/DECIMAL/NUMERIC/MONEY/SMALLMONEY → FLOAT64; BIT → BOOL; DATE/DATETIME/DATETIME2/SMALLDATETIME/DATETIMEOFFSET → TIMESTAMP; UNIQUEIDENTIFIER formatted to canonical 8-4-4-4-12 hex). SQL-fix prompt grounded in common T-SQL errors (Msg 208 invalid object name, Msg 8120 GROUP BY, Msg 4108 window functions, Msg 245 conversion failures, NOT IN NULL trap, OFFSET/FETCH requirements). Password credentials are URL-encoded in the DSN so `@`, `:`, `?`, `&`, `#`, `/`, and spaces are handled safely. Driver: `github.com/microsoft/go-mssqldb` (MIT). Integration tests use `testcontainers-go/modules/mssql` with the 2022 image; env-var gating (`INTEGRATION_TEST_MSSQL_*`) allows targeting an external SQL Server or Azure SQL Database.
- **Bookmark lists** — Create named lists and save insights or recommendations to them for later review. Add-to-list menu on every insight and recommendation detail page with inline list creation; new Lists nav entry with a grid of lists per project; list detail page shows items grouped by type with per-bookmark notes and a "remove" action. Deleting a list cascades to its bookmarks; the underlying insights and recommendations are unaffected. Orphan bookmarks (source deleted) render as "[removed]" rather than crashing.
- **Read tracking** — Opening an insight or recommendation now marks it read for the current user. List pages render read items with reduced opacity and a muted color, with a "Mark unread" action per row. State lives on the server (new `read_marks` collection) and is shared across devices in enterprise mode; in community mode all viewers share the single `"anonymous"` identity.
- **Technical details toggle** — The insight detail page's "How This Insight Was Found" section (SQL queries, exploration steps, token counts, validation queries) is now collapsed by default behind a "Show technical details" button. Non-technical readers get a clean narrative; power users click once to reveal the engine internals. No persistence — defaults to collapsed on every page visit.
- **Related items promoted + right sidebar TOC** — Related recommendations (on insight pages) and related insights (on recommendation pages) now appear in a sticky right-column TOC at the top of the viewport, alongside semantic-search similar items. On narrow screens the TOC collapses to a horizontally-scrollable chip strip above the main content. The inline mid-page "Related" cards and bottom "Similar" blocks were removed — everything lives in the sidebar now.
- **API endpoints** for the above: `POST/GET/PATCH/DELETE /api/v1/projects/{id}/lists`, `POST/DELETE /api/v1/projects/{id}/lists/{listId}/items`, `GET /api/v1/projects/{id}/bookmarks`, `POST/DELETE/GET /api/v1/projects/{id}/reads`. Every record carries a `user_id` field sourced from `auth.UserFromContext(ctx).Sub` — `"anonymous"` under NoAuth, the OIDC sub claim under enterprise. Same schema, same handlers in both modes; enterprise deployments get per-user scoping without schema migration.

### Fixed

- **Exploration terminated after 2–18 steps on reasoning-model LLMs** — The exploration response parser treated any response it couldn't cleanly decode as a "complete" signal: JSON without `query`/`done`/`action` was silently assumed done, and responses with no JSON fell through to a plain-text substring match on `"done"` / `"complete"` / `"finished"` — so a Qwen3 or DeepSeek-R1 thinking block mentioning *"I'm done analyzing area 1"* or *"the query completed"* ended the run. The parser is now strict: it walks every balanced JSON object (string-literal-aware, so `}` inside SQL strings no longer breaks the count), prefers the last block with a known action key (reasoning preambles no longer hijack the parse), and rejects anything without one. Unparseable responses are re-prompted up to three times with a reformat nudge instead of silently terminating. A new `MinSteps` exploration option (surfaced as `--min-steps` on the agent binary) rejects premature `done` signals, injects a nudge with the current/required step count, and continues — guarding against models biased toward early termination even with valid JSON. Covered by 40+ new unit tests including a Qwen3-style regression reproducing the original failure.

### Changed

- **Agent CLI: `--min-steps` flag** — New integer flag (default `0`, no floor) on `decisionbox-agent`. When non-zero, the agent rejects premature completion signals and records a `complete_rejected` step in the exploration log. Plumbed through `DiscoveryOptions.MinSteps` into `ai.ExplorationEngineOptions.MinSteps`.

- **Discovery API: `min_steps` option on `POST /api/v1/projects/{id}/discover`** — The request body now accepts an optional `min_steps` integer alongside `max_steps`. When omitted, the server applies a default of `floor(0.6 * max_steps)` — a conservative floor that still leaves headroom for genuinely short runs. Explicit `0` disables the floor; negative values and values exceeding `max_steps` return `400`. The handler forwards the resolved value to both the subprocess and Kubernetes runners, which append `--min-steps` to the agent's argv when positive. The dashboard's "Run discovery" menu gains a matching "Minimum steps" `NumberInput` that auto-tracks the 60% default until the user edits it; explicit `0` sends through as "disable floor" (the client uses `min_steps?: number` so a `0` survives the JSON round-trip instead of being dropped by a truthy check). Recommended for reasoning models (Qwen3, DeepSeek-R1, GPT-OSS) that tend to terminate exploration too early.

- **StepCallback signature** — Gains an `action string` parameter so downstream `StatusReporter.AddExplorationStep` can distinguish `query_data` steps from `complete_rejected` (min-steps rejection) events. The live UI now records rejected completions with `Type="complete_rejected"` and skips the query counter increment. Callers outside the agent don't need to change — the one internal caller in `services/agent/internal/discovery/orchestrator.go` was updated in the same change.

- **Docker Compose Quick Start: `decisionbox-agent` missing from API image** — The API container's default `RUNNER_MODE=subprocess` spawns the agent via `exec.Command("decisionbox-agent", ...)`, but `services/api/Dockerfile` only shipped the `decisionbox-api` binary. Starting a discovery failed with `exec: "decisionbox-agent": executable file not found in $PATH`. The API image now also builds and installs `decisionbox-agent` into `/usr/local/bin`, so `docker compose up -d` works end-to-end out of the box. Kubernetes deployments are unaffected — they use `RUNNER_MODE=kubernetes` and run the agent as a Job from its own image.

## [0.4.0] - 2026-04-14

### Added

- **Vector search stack** — Full semantic search and RAG-powered insight Q&A built on Qdrant. Includes 6 embedding providers (OpenAI, Vertex AI, Bedrock, Azure OpenAI, Voyage AI, Ollama), a Qdrant vector store provider with HNSW search, and an embedding-settings tab in the dashboard. Agent Phase 9 denormalizes insights and recommendations into standalone collections, generates embeddings, and upserts them into Qdrant. New API endpoints: `POST /search` (semantic search with filters), cross-project search, `POST /ask` (RAG-powered Ask Insights), multi-turn ask sessions, search history with 90-day TTL, and standalone insights/recommendations CRUD. Dashboard adds a Search page, an Ask Insights chat page with citations and session history, Spotlight search (Cmd+K), and a recommendation detail page. Infrastructure: Qdrant subchart in Helm (auto-compute URL, API key secret), setup wizard Step 6 (vector search config), and a `qdrant` service in `docker-compose.yml`.
- **Dynamic domain packs** — Domain packs are now stored in MongoDB instead of compiled Go code. Create, edit, import, and export packs from the dashboard without code changes. New CRUD endpoints at `/api/v1/domain-packs` with a portable JSON import/export format. Dashboard adds a Domain Packs management page with a markdown prompt editor. Built-in packs (gaming, ecommerce, social) are seeded from embedded JSON on first startup. Removed `DOMAIN_PACK_PATH` environment variable — packs no longer read from the filesystem. Agent no longer depends on domain pack code and reads prompts entirely from project configuration.
- **Webhook notifications** — Notify external systems when discoveries complete via Slack, generic HTTP, or email webhooks. Configurable per-project with templated payloads.
- **Anonymous usage telemetry** — Collects anonymous, privacy-respecting usage metrics (version, OS, provider types, event counts). Enabled by default, disable with `TELEMETRY_ENABLED=false` or `DO_NOT_TRACK=1`. No PII, no query content, no credentials. See [TELEMETRY.md](TELEMETRY.md) for full details.
- **Helm dashboard `extraEnv` / `extraEnvFrom`** — `decisionbox-dashboard` chart now supports `extraEnv` and `extraEnvFrom` values, bringing it to parity with the API chart. Enables overlays (e.g., enterprise auth) to inject env vars and secrets without modifying the chart. Chart version bumped `0.1.0 → 0.1.1`.

### Changed

- **`apiserver.Run()` now owns subcommand routing** — The `backfill-embeddings` subcommand is dispatched inside `apiserver.Run()` instead of from `services/api/main.go`. Custom API binaries (e.g., enterprise) that call `apiserver.Run()` automatically get all subcommands wired up, closing a gap where subcommands were unreachable from non-community entry points.

### Fixed

- **Go dependency security upgrades** — Resolved 18 Dependabot alerts (1 critical, 2 high, 15 medium) across 10 Go modules: `google.golang.org/grpc` (authorization bypass), `github.com/go-jose/go-jose/v3` (JWE decryption panic), `golang.org/x/oauth2` (input validation), and the AWS SDK v2 `bedrockruntime` / `eventstream` / `s3` packages (EventStream DoS). Dashboard Dockerfile now runs `apk upgrade --no-cache` to pick up Alpine security patches on rebuild.
- **IP allowlist wiring on AWS and GCP** — `setup.sh` was creating IP restriction resources in Terraform but never passing them to Helm, leaving the ALB / GCE ingress open to `0.0.0.0/0`. AWS now uses the `alb.ingress.kubernetes.io/inbound-cidrs` annotation (keeping the controller's backend SG management intact), and GCP now creates a `BackendConfig` CRD and annotates the dashboard Service with `cloud.google.com/backend-config` to attach the Cloud Armor policy.
- **Orphaned AWS `ip_allowlist` security group removed** — `aws_security_group.ip_allowlist` and its three supporting rules were no longer attached to anything after the switch to `inbound-cidrs`. Removed from the `terraform/aws` module along with the proxied output.
- **Terraform LLM IAM now granted to the API role, not just the agent** — `enable_bedrock_iam` / `enable_vertex_ai_iam` on AWS and GCP now also attach to the API's IRSA / Workload Identity SA. Previously the `/ask` endpoint (and any future API-side LLM call) returned 500 on EKS/GKE deployments with LLM IAM enabled because the API role had no `bedrock:*` or `aiplatform.user` permissions.
- **K8s test connection response parsing** — `extractJSONObject` now scans pod logs from the end and skips structured log lines (identified by the `"severity"` key). K8s pods mix stdout (result) with stderr (log lines), so the previous implementation picked up the first JSON line — a structured log, not the agent result — and the dashboard showed "Unknown error". Also adds `pods/log` to the `agent-job-manager` RBAC Role so the API can read agent pod logs.
- **Redshift SQL fix prompt** — The Redshift warehouse provider now returns a Redshift-specific self-healing prompt from `SQLFixPrompt()` (previously returned an empty string, so the agent's self-heal loop had no guidance for Redshift failures). Prompt covers the PostgreSQL features Redshift does not support (`DISTINCT ON`, `FILTER (WHERE ...)`, `LATERAL`, `generate_series`, `string_agg`, `array_agg`, `regexp_matches`, `FORMAT`), Redshift-native alternatives (`QUALIFY`, `LISTAGG`, `SUPER` + `json_extract_path_text`, `DATEADD`/`DATEDIFF`/`GETDATE`, `CONVERT_TIMEZONE`), and 17 common Redshift error patterns. `make lint-go` now includes the Redshift provider and CI has a matching `Lint Redshift warehouse provider` step.

### Removed

- Removed all Go code from `domain-packs/*/go/` directories (dynamic domain packs).
- Removed `libs/go-common/domainpack` package (registry, interfaces).
- Removed domain pack blank imports from agent and API server.

## [0.3.0] - 2026-04-06

### Added

- **PostgreSQL warehouse provider** — Connect to PostgreSQL databases with username/password or connection string authentication. Supports all common PostgreSQL data types including INTEGER, BIGINT, SERIAL, NUMERIC/DECIMAL (converted to float64), BOOLEAN, DATE, TIMESTAMP/TIMESTAMPTZ, BYTEA, JSON/JSONB, arrays, UUID, INET, and INTERVAL. Uses `information_schema` for table/column metadata and `pg_class.reltuples` for fast row count estimates. Includes comprehensive SQL fix prompt covering 13 error patterns (LATERAL joins, FILTER clause, recursive CTEs, NOT IN NULL trap, BETWEEN timestamp pitfall, and more). SSL mode configurable (default: `require`).
- **Databricks warehouse provider** — Connect to Databricks SQL warehouses via Unity Catalog with Personal Access Token or OAuth M2M (service principal) authentication. Uses the official `databricks-sql-go` driver with `NewConnector` structured options. Supports all Databricks data types including TINYINT through BIGINT, FLOAT/DOUBLE, DECIMAL (converted to float64), BOOLEAN, DATE, TIMESTAMP/TIMESTAMP_NTZ, BINARY, and complex types (STRUCT, ARRAY, MAP, VARIANT). Schema discovery via `catalog.information_schema`. Includes Databricks-specific SQL fix prompt covering QUALIFY, PIVOT/UNPIVOT, explode/explode_outer, Delta time travel, STRUCT/ARRAY/MAP access, and the `yyyy` vs `YYYY` date format pitfall.
- **Ecommerce domain pack** — Multi-category store analysis with 5 areas: conversion funnel, revenue & pricing, customer retention, product & category performance, and session & browsing behavior. Includes profile schema for store info, business model, fulfillment, marketing, and KPIs.
- **System-test domain pack** — Diagnostic domain pack for validating warehouse connectivity, schema discovery, data type mapping, and SQL dialect support. Not an industry pack — designed for testing and onboarding. Three categories by depth: quick (~10 queries), standard (~30-50 queries), thorough (~80-100 queries). Env-gated: only available when `DECISIONBOX_ENABLE_SYSTEM_TEST=true`.
- **Plugin middleware hooks** — Warehouse middleware (`warehouse.RegisterMiddleware()`) allows wrapping warehouse providers with custom logic such as logging, metrics, or access controls. HTTP middleware (`apiserver.RegisterGlobalMiddleware()`) allows wrapping all API requests. Agent startup logic exported as `agentserver.Run()` for custom builds. Context helpers `warehouse.WithProjectID()` / `ProjectIDFromContext()` for project-aware middleware.
- **Per-model max output token limits** — LLM provider metadata now includes `MaxOutputTokens` (model name → max output tokens). The agent's recommendation generation phase uses `gollm.GetMaxOutputTokens()` to request the model's full output capacity instead of a fixed 8K token limit. Lookup falls back to `_default` key, then to 8192.
- **Optional IP restriction for Terraform modules** — GKE, EKS, and AKS control plane access can be restricted to specific CIDR ranges via `allowed_cidr_blocks`. Setup wizard prompts for IP restriction and auto-detects the user's public IP.

### Fixed

- **Insight validation SQL fix parsing** — Fixed SQL fix prompt parsing that could fail when the LLM response contained extra formatting. Added missing schema context to validation queries, improving SQL fix success rate.

## [0.2.0] - 2026-03-29

### Added

- **Snowflake warehouse provider** — Connect to Snowflake data warehouses with username/password or key pair (JWT) authentication. Supports all Snowflake data types including NUMBER, FLOAT, BOOLEAN, DATE, TIMESTAMP (NTZ/LTZ/TZ), VARIANT, OBJECT, ARRAY, and BINARY. Uses INFORMATION_SCHEMA for metadata queries (no full-table scans for row counts). Includes Snowflake-specific SQL fix prompt for AI error correction.
- **Structured auth methods for warehouse providers** — Each warehouse provider declares its supported authentication methods via metadata. The dashboard renders an auth method selector with provider-specific fields. BigQuery supports ADC and Service Account Key. Redshift supports IAM Role, Access Keys, and Assume Role (with optional external ID for cross-account). Snowflake supports Username/Password and Key Pair (JWT).
- **Redshift external authentication** — Access Keys (`StaticCredentialsProvider`) and Assume Role (`stscreds.NewAssumeRoleProvider` with optional external ID) for cross-cloud and cross-account access.
- **Azure AI Foundry LLM provider** — Access Claude and OpenAI models through Microsoft Azure's managed AI platform. Routes to Anthropic Messages API (`/anthropic/v1/messages`) or OpenAI Chat Completions API (`/openai/v1/chat/completions`) based on model name. Supports API key authentication.
- **Azure Key Vault secret provider** — Store per-project secrets in Azure Key Vault with DefaultAzureCredential authentication (managed identity, Azure CLI, environment variables). Secret naming uses `{namespace}-{projectID}-{key}` format with managed-by tags for filtering.
- **Azure Terraform module** — Provision AKS, VNet, NAT Gateway, Managed Identities, and Key Vault on Azure. Follows the same module pattern as GCP and AWS. Includes Workload Identity federation, Container Insights, and deployment documentation.
- **Setup wizard Azure support** — The interactive setup wizard (`terraform/setup.sh`) now supports Azure as a third cloud provider. Handles `az login` authentication, Azure Blob Storage state backend, AKS credential configuration, Workload Identity annotations, and Key Vault integration.
- **Helm chart Azure Workload Identity** — Added `podLabels` support to API deployment template for `azure.workload.identity/use` label. Updated service account annotation examples for all three cloud providers (GCP, AWS, Azure).

### Changed

- **Credentials moved to contextual tabs** — Warehouse credentials and LLM API keys are now managed inline in their respective settings tabs (Data Warehouse, AI Provider). The standalone Secrets tab has been removed.

## [0.1.0] - 2026-03-23

Initial public release.

### Added

#### Core Platform
- AI-powered data discovery agent with autonomous SQL exploration
- REST API for project, discovery, and configuration management
- Web dashboard (Next.js) with live discovery progress, insights table, and recommendation cards
- Plugin architecture: providers register via `init()` with `RegisterWithMeta()`

#### LLM Providers
- Claude (direct API)
- OpenAI
- Ollama (local models)
- Vertex AI (Claude + Gemini on GCP)
- AWS Bedrock (Claude on AWS)

#### Warehouse Providers
- Google BigQuery (with dry-run cost estimation)
- Amazon Redshift (serverless + provisioned)

#### Secret Providers
- MongoDB (AES-256-GCM encryption)
- GCP Secret Manager
- AWS Secrets Manager

#### Domain Packs
- Gaming: 3 categories (match-3, idle/incremental, casual/hyper-casual) with 5 analysis areas each
- Social Network: content sharing category with 5 analysis areas (growth, engagement, retention, content creation, monetization)
- Pluggable architecture with areas.json, prompt templates, and JSON Schema profiles

#### Discovery Features
- Per-project editable prompts and custom analysis areas
- Discovery cost estimation (LLM tokens + warehouse query costs)
- Insight validation (AI claims verified against actual data)
- Feedback system (like/dislike with comments on insights and recommendations)
- Context-aware discoveries (agent learns from previous runs and user feedback)
- Recommendation-to-insight linking with cross-references in UI
- Selective discovery (run specific analysis areas)
- Live discovery progress with phase tracking, step details, and expandable SQL
- Test Connection buttons for LLM and warehouse providers

#### Infrastructure
- K8s runner for production (API creates K8s Jobs per discovery)
- Subprocess runner for local development
- Docker Compose setup for local development
- Helm charts for Kubernetes deployment (API, Dashboard, optional MongoDB subchart)
- Public Helm chart repository at `https://decisionbox-io.github.io/decisionbox-platform`
- GCP Terraform module (GKE, VPC, IAM, Workload Identity, BigQuery)
- AWS Terraform module (EKS, VPC, IAM, IRSA, Secrets Manager, Redshift)
- Interactive setup wizard (`terraform/setup.sh`) with auth, resume, and destroy support
- Multi-arch Docker images (linux/amd64 + linux/arm64)

#### CI/CD
- GitHub Actions: build, test, lint (Go + Dashboard)
- Docker image build with SBOM generation and vulnerability scanning
- License compliance check (Anchore Grant)
- CLA bot for contributor agreements
- Codecov integration with unit + integration test coverage

#### Quality
- 500+ tests (unit, integration, mock-based, testcontainers)
- 85%+ unit test coverage across all modules
- Comprehensive documentation (28 files across 6 sections)

[Unreleased]: https://github.com/decisionbox-io/decisionbox-platform/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/decisionbox-io/decisionbox-platform/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/decisionbox-io/decisionbox-platform/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/decisionbox-io/decisionbox-platform/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/decisionbox-io/decisionbox-platform/releases/tag/v0.1.0
