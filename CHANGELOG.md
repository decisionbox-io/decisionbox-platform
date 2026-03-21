# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- AI-powered data discovery agent with autonomous SQL exploration
- REST API for project, discovery, and configuration management
- Web dashboard with live discovery progress, insights table, and recommendation cards
- LLM providers: Claude, OpenAI, Ollama, Vertex AI (Claude + Gemini), Bedrock
- Warehouse providers: BigQuery (with dry-run cost estimation), Amazon Redshift (serverless + provisioned)
- Secret providers: MongoDB (AES-256), GCP Secret Manager, AWS Secrets Manager
- Gaming domain pack with 3 categories (match-3, idle/incremental, casual/hyper-casual) and 5 analysis areas per category
- Social network domain pack with content sharing category and 5 analysis areas (growth, engagement, retention, content creation, monetization)
- Pluggable domain pack architecture with areas.json, prompt templates, and JSON Schema profiles
- Per-project editable prompts and custom analysis areas
- Discovery cost estimation (LLM tokens + warehouse query costs)
- Insight validation (AI claims verified against actual data)
- Feedback system (like/dislike with comments on insights and recommendations)
- Context-aware discoveries (agent learns from previous runs and user feedback)
- Recommendation-to-insight linking with cross-references in UI
- Selective discovery (run specific analysis areas)
- Live discovery progress with phase tracking, step details, and expandable SQL
- Test Connection buttons for LLM and warehouse providers in project settings
- LLM API key input during project creation (conditional — only for providers that need one)
- `Validate(ctx)` method on LLM Provider interface for credential verification
- `RunSync` on Runner interface for synchronous agent invocations (test connection)
- K8s runner for production (API creates K8s Jobs per discovery)
- Subprocess runner for local development
- Docker Compose setup for local development
- Helm charts for Kubernetes deployment (API, Dashboard, MongoDB subchart)
- Public Helm chart repository at `https://decisionbox-io.github.io/decisionbox-platform`
- GCP Terraform module (GKE, VPC, IAM, BigQuery)
- Multi-arch Docker images (linux/amd64 + linux/arm64)
- GitHub Actions CI for Docker image builds
- 350+ tests (unit, integration, testcontainers)
- Comprehensive documentation (28 files across 6 sections)

## [0.1.0] - TBD

Initial public release.
