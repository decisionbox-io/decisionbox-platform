<p align="center">
  <img src="assets/logos/logo-light@2x.png" alt="DecisionBox" width="400" />
</p>

<p align="center">
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-AGPL_v3-blue.svg" alt="License: AGPL v3" /></a>
  <a href="https://golang.org"><img src="https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white" alt="Go" /></a>
  <a href="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/ci.yml"><img src="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/docker-publish.yml"><img src="https://github.com/decisionbox-io/decisionbox-platform/actions/workflows/docker-publish.yml/badge.svg" alt="Docker Build" /></a>
  <a href="https://codecov.io/gh/decisionbox-io/decisionbox-platform"><img src="https://codecov.io/gh/decisionbox-io/decisionbox-platform/graph/badge.svg" alt="Coverage" /></a>
  <a href="https://github.com/decisionbox-io/decisionbox-platform/releases/latest"><img src="https://img.shields.io/github/v/release/decisionbox-io/decisionbox-platform" alt="Latest Release" /></a>
  <a href="https://github.com/decisionbox-io/decisionbox-platform/issues"><img src="https://img.shields.io/github/issues/decisionbox-io/decisionbox-platform" alt="Issues" /></a>
  <a href="CONTRIBUTING.md"><img src="https://img.shields.io/badge/PRs-welcome-brightgreen.svg" alt="PRs Welcome" /></a>
  <a href="https://decisionbox.io/docs"><img src="https://img.shields.io/badge/docs-decisionbox.io-blue" alt="Docs" /></a>
</p>

<p align="center">
  <a href="https://www.producthunt.com/products/decisionbox?embed=true&amp;utm_source=badge-featured&amp;utm_medium=badge&amp;utm_campaign=badge-decisionbox" target="_blank" rel="noopener noreferrer"><img alt="DecisionBox - Autonomous AI Discovery For Your Data — Open Source | Product Hunt" width="250" height="54" src="https://api.producthunt.com/widgets/embed-image/v1/featured.svg?post_id=1102341&amp;theme=light&amp;t=1774002189886"></a>
</p>

**AI-powered data discovery platform.** Connect your data warehouse, run AI agents that explore your data autonomously, and get actionable insights — no pipelines, no SDKs, no setup complexity.

DecisionBox is designed for product managers, analysts, and developers who want to understand their data without writing SQL or building dashboards. Point it at your warehouse, tell it about your product, and let the AI find what matters.

<p align="center">
  <img src="assets/screenshot.png" alt="DecisionBox Dashboard" width="900" />
</p>

## How It Works

```
Your Data Warehouse             DecisionBox Agent            Dashboard
(BigQuery, Redshift,      →    (AI explores your data)  →   (Insights & Recommendations)
 Snowflake, PostgreSQL,
 Databricks)
                                 writes SQL, validates
                                 findings, generates
                                 actionable advice
```

1. **Connect** your data warehouse (BigQuery, Redshift, Snowflake, PostgreSQL, Databricks, and more)
2. **Configure** your project (domain, game profile, LLM provider)
3. **Run discovery** — the AI agent autonomously explores your data
4. **Review insights** — severity-ranked findings with confidence scores
5. **Act on recommendations** — specific, numbered action steps

## Features

- **Autonomous data exploration** — AI writes and executes SQL queries, iterates based on results
- **Domain-aware analysis** — Pluggable domain packs (gaming, social network shipped — bring your own)
- **Multiple LLM providers** — Claude, OpenAI, Ollama, Vertex AI, Bedrock, Azure AI Foundry
- **Multiple warehouses** — BigQuery, Amazon Redshift (serverless + provisioned), Snowflake, PostgreSQL, Databricks
- **Per-project secrets** — API keys encrypted per-project (MongoDB, GCP Secret Manager, AWS Secrets Manager, Azure Key Vault)
- **Insight validation** — AI claims are verified against your actual data
- **Feedback loop** — Like/dislike insights, agent learns from feedback on next run
- **Cost estimation** — Estimate LLM + warehouse costs before running
- **Live progress** — Watch the agent explore in real-time with step-by-step updates
- **Editable prompts** — Customize all AI prompts per-project from the dashboard
- **Extensible** — Add your own domain packs, LLM providers, warehouse providers via plugin architecture

## Use Cases

DecisionBox works with any queryable data. Point it at your data source and it discovers insights specific to your domain.

**Gaming** — _"Players who fail level 12 more than 3 times have 68% higher Day-7 churn. Consider adding a hint system or difficulty adjustment at this stage."_

**Social Network** — _"Posts published between 6–8 PM with images get 3.2x more shares, but only 12% of creators post during this window. A scheduling nudge could boost platform-wide engagement."_

**E-commerce** — _"Cart abandonment spikes 40% when shipping cost exceeds 8% of cart value. Free shipping threshold at $75 would recover an estimated 1,200 orders/month."_

**Fraud Detection** — _"Accounts created in the last 48 hours with 5+ high-value transactions account for 82% of chargebacks. Flagging this pattern would prevent $34K/month in losses."_

**SaaS** — _"Teams that don't use the dashboard feature within 14 days of signup have 3x higher churn. An onboarding email on Day 3 highlighting dashboards could improve activation."_

**SQL Performance** — _"The top 10 slowest queries consume 62% of warehouse compute. 7 of them scan full tables where a partition filter would reduce cost by ~$4,800/month."_

These are examples — create a [domain pack](https://decisionbox.io/docs/guides/creating-domain-packs) for any industry and DecisionBox adapts its analysis accordingly.

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
- A data warehouse connection (BigQuery project ID, Redshift workgroup, Snowflake account, or Databricks workspace)
- An LLM API key (Anthropic, OpenAI, or configure Vertex AI / Bedrock)

For detailed setup instructions, see the [Installation Guide](https://decisionbox.io/docs/getting-started/installation).

## Deployment

| Method | Use case | Guide |
|--------|----------|-------|
| **Docker Compose** | Local dev, single server | [Docker](https://decisionbox.io/docs/deployment/docker) |
| **Kubernetes (Helm)** | Production on any K8s cluster | [Kubernetes](https://decisionbox.io/docs/deployment/kubernetes) |
| **Terraform (GCP)** | Automated GKE provisioning | [Terraform GCP](https://decisionbox.io/docs/deployment/terraform-gcp) |
| **Terraform (AWS)** | Automated EKS provisioning | [Terraform AWS](https://decisionbox.io/docs/deployment/terraform-aws) |
| **Terraform (Azure)** | Automated AKS provisioning | [Terraform Azure](https://decisionbox.io/docs/deployment/terraform-azure) |
| **Setup Wizard** | One-command GKE/EKS/AKS + Helm deploy | [Setup Wizard](https://decisionbox.io/docs/deployment/setup-wizard) |

Resources: [Helm charts](helm-charts/) | [Terraform modules](terraform/) | [Helm values reference](https://decisionbox.io/docs/reference/helm-values)

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

See [Creating Domain Packs](https://decisionbox.io/docs/guides/creating-domain-packs).

### LLM Providers

Add support for any LLM by implementing the `llm.Provider` interface (one method: `Chat`).

See [Adding LLM Providers](https://decisionbox.io/docs/guides/adding-llm-providers).

### Warehouse Providers

Add support for any SQL warehouse by implementing the `warehouse.Provider` interface.

See [Adding Warehouse Providers](https://decisionbox.io/docs/guides/adding-warehouse-providers).

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

Full reference: [Configuration](https://decisionbox.io/docs/reference/configuration).

## Documentation

**[decisionbox.io/docs](https://decisionbox.io/docs/)** — Full documentation including:

- [Quick Start](https://decisionbox.io/docs/getting-started/quickstart)
- [Installation Guide](https://decisionbox.io/docs/getting-started/installation)
- [Architecture](https://decisionbox.io/docs/concepts/architecture)
- [Providers](https://decisionbox.io/docs/concepts/providers)
- [Domain Packs](https://decisionbox.io/docs/concepts/domain-packs)
- [API Reference](https://decisionbox.io/docs/reference/api)
- [Configuration Reference](https://decisionbox.io/docs/reference/configuration)
- [Contributing](https://decisionbox.io/docs/contributing/development)

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Agent | Go 1.25 |
| API | Go 1.25, net/http (stdlib) |
| Dashboard | Next.js 16, React 19, TypeScript, Mantine 8 |
| Database | MongoDB |
| CI/CD | GitHub Actions, GHCR |
| Deployment | Docker Compose, Kubernetes (Helm), Terraform (GCP, AWS, Azure) |

## Contributing

We welcome contributions. See [Contributing Guide](https://decisionbox.io/docs/contributing/development) for development setup, testing, and PR process.

## Community

- [GitHub Issues](https://github.com/decisionbox-io/decisionbox-platform/issues) — Bug reports, feature requests
- [GitHub Discussions](https://github.com/decisionbox-io/decisionbox-platform/discussions) — Questions, ideas

## Roadmap

See the full roadmap on the [project board](https://github.com/orgs/decisionbox-io/projects/4/views/3).
