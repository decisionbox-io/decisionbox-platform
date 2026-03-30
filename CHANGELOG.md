# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/decisionbox-io/decisionbox-platform/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/decisionbox-io/decisionbox-platform/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/decisionbox-io/decisionbox-platform/releases/tag/v0.1.0
