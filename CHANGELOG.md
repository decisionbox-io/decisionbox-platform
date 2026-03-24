# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Snowflake warehouse provider** — Connect to Snowflake data warehouses with username/password or key pair (JWT) authentication. Supports all Snowflake data types including NUMBER, FLOAT, BOOLEAN, DATE, TIMESTAMP (NTZ/LTZ/TZ), VARIANT, OBJECT, ARRAY, and BINARY. Uses INFORMATION_SCHEMA for metadata queries (no full-table scans for row counts). Includes Snowflake-specific SQL fix prompt for AI error correction.

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

[Unreleased]: https://github.com/decisionbox-io/decisionbox-platform/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/decisionbox-io/decisionbox-platform/releases/tag/v0.1.0
