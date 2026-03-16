# DecisionBox Documentation

> **Version**: 0.1.0

DecisionBox is an open-source AI-powered data discovery platform. It connects to your data warehouse, runs autonomous AI agents that explore your data, and surfaces actionable insights and recommendations.

## Who Is This For?

- **Product managers** who want data-driven insights without writing SQL
- **Data analysts** who want AI to augment their exploration
- **Developers** building data products who need automated pattern detection
- **Game studios** analyzing player behavior, churn, monetization (gaming domain pack)
- **Social platforms** analyzing user growth, engagement, content creation, and premium features (social network domain pack)

## What Can It Do?

- Connect to BigQuery or Amazon Redshift (more warehouses coming)
- Run AI agents that write SQL, analyze results, and iterate autonomously
- Discover patterns across churn, engagement, monetization, and domain-specific areas
- Validate findings against actual data (not just LLM hallucination)
- Generate specific, numbered action steps — not generic advice
- Learn from your feedback — liked and disliked insights inform the next run
- Estimate costs before running (LLM tokens + warehouse query costs)

## Documentation Structure

### Getting Started

New to DecisionBox? Start here.

- [Quick Start](getting-started/quickstart.md) — Docker Compose to first discovery in 5 minutes
- [Installation](getting-started/installation.md) — All installation methods
- [Your First Discovery](getting-started/first-discovery.md) — End-to-end walkthrough

### Concepts

Understand how DecisionBox works.

- [Architecture](concepts/architecture.md) — System components and data flow
- [Discovery Lifecycle](concepts/discovery-lifecycle.md) — What happens during a discovery run
- [Domain Packs](concepts/domain-packs.md) — How domain-specific analysis works
- [Providers](concepts/providers.md) — Plugin architecture for LLM, warehouse, and secrets
- [Prompts](concepts/prompts.md) — How AI prompts work, template variables, customization

### Guides

Step-by-step instructions for common tasks.

- [Creating Domain Packs](guides/creating-domain-packs.md) — Build analysis for your industry
- [Adding LLM Providers](guides/adding-llm-providers.md) — Support a new LLM service
- [Adding Warehouse Providers](guides/adding-warehouse-providers.md) — Support a new data warehouse
- [Adding Secret Providers](guides/adding-secret-providers.md) — Support a new secret manager
- [Configuring LLM Providers](guides/configuring-llm.md) — Claude, OpenAI, Ollama, Vertex AI, Bedrock
- [Configuring Warehouses](guides/configuring-warehouse.md) — BigQuery, Redshift setup
- [Configuring Secrets](guides/configuring-secrets.md) — Encrypted key management
- [Customizing Prompts](guides/customizing-prompts.md) — Edit prompts, add analysis areas
- [Project Profiles](guides/project-profiles.md) — How profiles improve insight quality

### Reference

Detailed specifications.

- [API Reference](reference/api.md) — All REST endpoints with examples
- [Configuration](reference/configuration.md) — All environment variables
- [CLI Reference](reference/cli.md) — Agent command-line flags
- [Prompt Variables](reference/prompt-variables.md) — Template variable reference
- [Data Models](reference/data-models.md) — Insight, Recommendation, Discovery models
- [Makefile Targets](reference/makefile.md) — Build, test, dev commands

### Deployment

Run DecisionBox in production.

- [Docker Compose](deployment/docker.md) — Full deployment guide
- [Production Considerations](deployment/production.md) — Security, scaling, monitoring

### Contributing

Help improve DecisionBox.

- [Development Setup](contributing/development.md) — Local dev environment
- [Testing](contributing/testing.md) — Test suite and writing tests
- [Pull Requests](contributing/pull-requests.md) — PR process and conventions
