<p align="center">
  <img src="assets/logos/logo-light@2x.png" alt="DecisionBox" width="400" />
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-AGPL_v3-blue.svg" alt="License: AGPL v3" /></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go" /></a>
  <a href="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/ci.yml"><img src="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/docker-publish.yml"><img src="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/docker-publish.yml/badge.svg" alt="Docker Build" /></a>
  <a href="https://github.com/decisionbox-io/decisionbox-platform/issues"><img src="https://img.shields.io/github/issues/decisionbox-io/decisionbox-platform" alt="Issues" /></a>
  <a href="CONTRIBUTING.md"><img src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg" alt="PRs Welcome" /></a>
  <a href="https://decisionbox.io/docs"><img src="https://img.shields.io/badge/docs-decisionbox.io-blue" alt="Docs" /></a>
</p>

**AI-powered data discovery platform.** Connect your data warehouse, run AI agents that explore your data autonomously, and get actionable insights — no pipelines, no SDKs, no setup complexity.

DecisionBox is designed for product managers, analysts, and developers who want to understand their data without writing SQL or building dashboards. Point it at your warehouse, tell it about your product, and let the AI find what matters.

<p align="center">
  <img src="assets/screenshot.png" alt="DecisionBox Dashboard" width="900" />
</p>

## How It Works

```
Your Data Warehouse             DecisionBox Agent            Dashboard
(BigQuery, Redshift, ...)  →    (AI explores your data)  →   (Insights & Recommendations)
                                 writes SQL, validates
                                 findings, generates
                                 actionable advice
```

1. **Connect** your data warehouse (BigQuery, Redshift, and more)
2. **Configure** your project (domain, game profile, LLM provider)
3. **Run discovery** — the AI agent autonomously explores your data
4. **Review insights** — severity-ranked findings with confidence scores
5. **Act on recommendations** — specific, numbered action steps

## Features

- **Autonomous data exploration** — AI writes and executes SQL queries, iterates based on results
- **Domain-aware analysis** — Pluggable domain packs (gaming, social network shipped — bring your own)
- **Multiple LLM providers** — Claude, OpenAI, Ollama, Vertex AI, Bedrock
- **Multiple warehouses** — BigQuery, Amazon Redshift (serverless + provisioned)
- **Per-project secrets** — API keys encrypted per-project (MongoDB, GCP Secret Manager, AWS Secrets Manager)
- **Insight validation** — AI claims are verified against your actual data
- **Feedback loop** — Like/dislike insights, agent learns from feedback on next run
- **Cost estimation** — Estimate LLM + warehouse costs before running
- **Live progress** — Watch the agent explore in real-time with step-by-step updates
- **Editable prompts** — Customize all AI prompts per-project from the dashboard
- **Extensible** — Add your own domain packs, LLM providers, warehouse providers via plugin architecture

## Quick Start

**Prerequisites:** Docker and Docker Compose

```bash
# Clone the repository
git clone https://github.com/decisionbox-io/decisionbox-platform.git
cd decisionbox-platform

# Start MongoDB + API + Dashboard
docker compose up -d

# Open the dashboard
open http://localhost:3000
```

The dashboard will guide you through creating your first project. You'll need:
- A data warehouse connection (BigQuery project ID or Redshift workgroup)
- An LLM API key (Anthropic, OpenAI, or configure Vertex AI / Bedrock)

For detailed setup instructions, see the [Installation Guide](docs/getting-started/installation.md).

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Dashboard (Next.js)                │
│              http://localhost:3000                    │
│    Project setup, insights, recommendations, live    │
│    progress, prompt editing, feedback                │
└──────────────────────┬──────────────────────────────┘
                       │ /api/* proxy
                       ▼
┌─────────────────────────────────────────────────────┐
│                    API (Go)                          │
│              http://localhost:8080                    │
│    Projects, discoveries, secrets, health,           │
│    spawns agent as subprocess or K8s Job             │
└───────┬──────────────────────────────┬──────────────┘
        │ spawns                       │ reads/writes
        ▼                              ▼
┌────────────────────┐        ┌────────────────────┐
│   Agent (Go)       │        │   MongoDB          │
│   Autonomous AI    │───────▶│   Projects,        │
│   data explorer    │ writes │   discoveries,     │
│                    │        │   secrets, runs     │
│   - LLM provider  │        └────────────────────┘
│   - Warehouse      │
│   - Domain pack    │
│   - Prompts        │
└────────┬───────────┘
         │ SQL queries
         ▼
┌────────────────────┐
│  Data Warehouse    │
│  (BigQuery /       │
│   Redshift / ...)  │
└────────────────────┘
```

**Only infrastructure dependency:** MongoDB. No Kafka, Redis, or RabbitMQ.

## Project Structure

```
decisionbox-platform/
├── services/
│   ├── agent/          # AI discovery agent (Go)
│   └── api/            # REST API (Go)
├── ui/
│   └── dashboard/      # Web dashboard (Next.js 16, React 19, Mantine 8)
├── libs/
│   └── go-common/      # Shared Go libraries (LLM, warehouse, secrets interfaces)
├── providers/
│   ├── llm/            # LLM providers (claude, openai, ollama, vertex-ai, bedrock)
│   ├── warehouse/      # Warehouse providers (bigquery, redshift)
│   └── secrets/        # Secret providers (mongodb, gcp, aws)
├── domain-packs/
│   ├── gaming/         # Gaming domain pack (match-3, idle, casual)
│   └── social/         # Social network domain pack (content sharing)
├── docs/               # Documentation
├── docker-compose.yml  # Local development stack
├── Makefile            # Build, test, dev commands
└── .github/
    └── workflows/      # CI/CD (Docker image builds)
```

## Development

**Run locally without Docker** (recommended for development):

```bash
# Start MongoDB only
docker compose up -d mongodb

# Terminal 1: Run the API
make dev-api

# Terminal 2: Run the Dashboard
make dev-dashboard

# Open http://localhost:3000
```

**Build binaries:**

```bash
make build              # Build agent + API binaries
make build-dashboard    # Build dashboard
```

**Run tests:**

```bash
make test               # All tests (Go + UI)
make test-go            # Go unit tests only
make test-integration   # Integration tests (needs Docker)
make test-llm           # LLM provider tests (needs API keys)
```

## Extending DecisionBox

DecisionBox is built on a plugin architecture. You can add:

### Domain Packs

Domain packs define how the AI analyzes data for a specific industry. A domain pack includes:
- Analysis areas (what to look for)
- Prompt templates (how the AI reasons)
- Profile schemas (what context users provide)

See [Creating Domain Packs](docs/guides/creating-domain-packs.md).

### LLM Providers

Add support for any LLM by implementing the `llm.Provider` interface (one method: `Chat`).

See [Adding LLM Providers](docs/guides/adding-llm-providers.md).

### Warehouse Providers

Add support for any SQL warehouse by implementing the `warehouse.Provider` interface.

See [Adding Warehouse Providers](docs/guides/adding-warehouse-providers.md).

## Configuration

Key environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `MONGODB_URI` | (required) | MongoDB connection string |
| `MONGODB_DB` | `decisionbox` | Database name |
| `SECRET_PROVIDER` | `mongodb` | Secret storage: `mongodb`, `gcp`, `aws` |
| `RUNNER_MODE` | `subprocess` | Agent runner: `subprocess`, `kubernetes` |
| `DOMAIN_PACK_PATH` | `/app/domain-packs` | Path to domain pack files |
| `LLM_TIMEOUT` | `300s` | Timeout per LLM API call |

Full reference: [Configuration](docs/reference/configuration.md).

## Documentation

- [Quick Start](docs/getting-started/quickstart.md)
- [Installation Guide](docs/getting-started/installation.md)
- [Architecture](docs/concepts/architecture.md)
- [API Reference](docs/reference/api.md)
- [Configuration Reference](docs/reference/configuration.md)
- [Creating Domain Packs](docs/guides/creating-domain-packs.md)
- [Adding Providers](docs/guides/adding-llm-providers.md)
- [Contributing](docs/contributing/development.md)

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Agent | Go 1.25 |
| API | Go 1.25, net/http (stdlib) |
| Dashboard | Next.js 16, React 19, TypeScript, Mantine 8 |
| Database | MongoDB |
| LLM | Claude, OpenAI, Ollama, Vertex AI, Bedrock |
| Warehouse | BigQuery, Amazon Redshift |
| CI/CD | GitHub Actions, GHCR |
| Deployment | Docker Compose, Kubernetes (Helm), Terraform (GCP) |

## Contributing

We welcome contributions. See [Contributing Guide](docs/contributing/development.md) for development setup, testing, and PR process.

## Community

- [GitHub Issues](https://github.com/decisionbox-io/decisionbox-platform/issues) — Bug reports, feature requests
- [GitHub Discussions](https://github.com/decisionbox-io/decisionbox-platform/discussions) — Questions, ideas

## Deployment

| Method | Use case | Guide |
|--------|----------|-------|
| **Docker Compose** | Local dev, single server | [docs/deployment/docker.md](docs/deployment/docker.md) |
| **Kubernetes (Helm)** | Production on any K8s cluster | [docs/deployment/kubernetes.md](docs/deployment/kubernetes.md) |
| **Terraform (GCP)** | Automated GKE provisioning | [docs/deployment/terraform-gcp.md](docs/deployment/terraform-gcp.md) |

## Roadmap

- Terraform module for AWS (EKS)
- More warehouse providers (PostgreSQL, Snowflake, Databricks)
- More domain packs (e-commerce, SaaS, fintech, education)
- Natural language queries ("Ask your data")
- Scheduled discovery runs (cron)
- Multi-user authentication
