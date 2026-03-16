# Contributing to DecisionBox

Thank you for your interest in contributing to DecisionBox! This document covers everything you need to know to get started.

## Table of Contents

- [Code of Conduct](#code-of-conduct)
- [Contributor License Agreement](#contributor-license-agreement)
- [How to Contribute](#how-to-contribute)
- [Getting Started](#getting-started)
- [Development Workflow](#development-workflow)
- [What to Contribute](#what-to-contribute)
- [Architecture Overview](#architecture-overview)
- [Coding Standards](#coding-standards)
- [Testing Requirements](#testing-requirements)
- [Pull Request Process](#pull-request-process)
- [Issue Guidelines](#issue-guidelines)
- [Community](#community)

## Code of Conduct

This project follows the [Contributor Covenant Code of Conduct](CODE_OF_CONDUCT.md). By participating, you agree to uphold this code. Please report unacceptable behavior to conduct@decisionbox.io.

## Contributor License Agreement

A CLA is required **only for code contributions** (pull requests). You do **not** need to sign a CLA to:
- Open issues or bug reports
- Participate in discussions
- Review pull requests
- Suggest features or ideas

When you submit your first pull request, a CLA bot will ask you to sign the [Contributor License Agreement](CLA.md). This is a one-time process.

**Why a CLA?** The CLA allows DecisionBox to distribute your contributions under the project license (AGPL v3) and, in the future, under additional license terms if needed. You retain full copyright to your contributions and can use them in any other project.

## How to Contribute

### Reporting Bugs

1. **Search existing issues** -- Someone may have already reported it
2. Click **New Issue** and select the **Bug Report** template
3. Include: steps to reproduce, expected vs actual behavior, environment details, and logs
4. Add relevant labels (`component: agent`, `component: api`, etc.)

### Suggesting Features

1. **Check [Discussions > Ideas](https://github.com/decisionbox-io/decisionbox-platform/discussions)** -- It may already be discussed
2. For small changes, open an issue with the **Feature Request** template
3. For larger changes, start a Discussion first to gather feedback before writing code

### Improving Documentation

Documentation contributions are highly valued. You can:
- Fix typos, clarify wording, add examples
- Add missing documentation for existing features
- Improve guides with real-world scenarios
- No issue needed for doc-only changes -- just open a PR

### Submitting Code

1. Fork the repository
2. Create a feature branch from `main`
3. Make your changes with tests
4. Submit a pull request

Details below.

## Getting Started

### Prerequisites

- **Go 1.25+** -- [golang.org/dl](https://golang.org/dl/)
- **Node.js 20+** -- [nodejs.org](https://nodejs.org/)
- **Docker** -- For MongoDB and integration tests
- **Make** -- Build tool (pre-installed on macOS/Linux)

### Clone and Build

```bash
# Fork on GitHub, then clone your fork
git clone https://github.com/YOUR-USERNAME/decisionbox-platform.git
cd decisionbox-platform

# Add upstream remote
git remote add upstream https://github.com/decisionbox-io/decisionbox-platform.git

# Build all Go binaries
make build

# Install agent binary (API spawns it as subprocess in dev mode)
sudo cp bin/decisionbox-agent /usr/local/bin/

# Install dashboard dependencies
cd ui/dashboard && npm install && cd ../..
```

### Run Locally

```bash
# Start MongoDB
docker compose up -d mongodb

# Terminal 1: Start the API
make dev-api

# Terminal 2: Start the Dashboard
make dev-dashboard
```

Open **http://localhost:3000** -- you should see the DecisionBox dashboard.

For the full development setup guide including environment variables, manual agent runs, and module structure, see [docs/contributing/development.md](docs/contributing/development.md).

## Development Workflow

```bash
# 1. Sync your fork with upstream
git checkout main
git pull upstream main

# 2. Create a feature branch
git checkout -b feat/my-feature

# 3. Make changes...

# 4. Run tests and lint
make test-go          # Go unit tests
make test-ui          # Dashboard tests
make lint             # golangci-lint + ESLint

# 5. Commit using conventional commits
git add -A
git commit -m "feat(warehouse): add Snowflake provider"

# 6. Push and open a PR
git push origin feat/my-feature
```

### Commit Message Format

We follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <subject>
```

**Types**: `feat`, `fix`, `docs`, `test`, `refactor`, `chore`

**Scopes** (optional): `agent`, `api`, `ui`, `llm`, `warehouse`, `secrets`, `domain-packs`, `infra`

**Examples**:
```
feat(warehouse): add Snowflake provider
fix(agent): handle LLM timeout during analysis phase
docs: add Snowflake configuration guide
test(llm): add Vertex AI integration tests
chore(infra): update Helm chart resource defaults
```

## What to Contribute

### Good Starting Points

- Issues labeled [`good first issue`](https://github.com/decisionbox-io/decisionbox-platform/labels/good%20first%20issue) -- small, well-scoped tasks for newcomers
- Issues labeled [`help wanted`](https://github.com/decisionbox-io/decisionbox-platform/labels/help%20wanted) -- larger tasks where help is needed
- The [project board](https://github.com/orgs/decisionbox-io/projects/4) shows the full roadmap

### Contribution Areas

#### New Providers

DecisionBox uses a plugin architecture. You can add:

- **LLM providers** -- Connect new AI models (see [Adding LLM Providers](docs/guides/adding-llm-providers.md))
- **Warehouse providers** -- Support new data warehouses (see [Adding Warehouse Providers](docs/guides/adding-warehouse-providers.md))
- **Secret providers** -- Add secret management backends (see [Adding Secret Providers](docs/guides/adding-secret-providers.md))

All providers follow the same pattern: implement an interface, register via `init()`, and add tests. Existing providers serve as complete examples.

#### New Domain Packs

Domain packs teach the AI agent how to analyze specific types of data. The gaming/match-3 domain pack ships as a complete reference. A domain pack includes:

- Analysis area definitions (`areas.json`)
- LLM prompts for exploration, analysis, and recommendations (Markdown files)
- Project profile schema (JSON Schema for domain-specific configuration)

See [Creating Domain Packs](docs/guides/creating-domain-packs.md) for a full tutorial with an e-commerce example.

#### Core Improvements

- Agent discovery logic (`services/agent/`)
- API endpoints and handlers (`services/api/`)
- Dashboard UI components (`ui/dashboard/`)
- Shared libraries (`libs/go-common/`)

For core changes, please open an issue first to discuss the approach.

## Architecture Overview

```
Your Data Warehouse          DecisionBox Agent          Dashboard
(BigQuery, Redshift)    ->   (AI explores your data)  ->  (Insights & Recommendations)
```

| Component | Location | Tech |
|-----------|----------|------|
| Agent | `services/agent/` | Go -- runs discovery, talks to LLM + warehouse |
| API | `services/api/` | Go -- REST API, manages projects/runs/settings |
| Dashboard | `ui/dashboard/` | Next.js -- web UI, proxies `/api/*` to API backend |
| Shared libs | `libs/go-common/` | Go -- interfaces for LLM, warehouse, secrets, health |
| Providers | `providers/` | Go -- LLM, warehouse, secret implementations |
| Domain packs | `domain-packs/` | Markdown + JSON -- prompts, areas, profile schemas |

The project uses Go workspaces. Each provider and service has its own `go.mod` with `replace` directives pointing to local modules. For the full architecture, see [docs/concepts/architecture.md](docs/concepts/architecture.md).

### Key Files

| File | Purpose |
|------|---------|
| `services/api/internal/server/server.go` | All route registrations |
| `services/agent/internal/discovery/orchestrator.go` | Discovery flow (exploration, analysis, recommendations) |
| `services/agent/main.go` | Agent startup and provider initialization |
| `libs/go-common/llm/registry.go` | LLM provider interface + registry |
| `libs/go-common/warehouse/provider.go` | Warehouse provider interface |
| `libs/go-common/secrets/provider.go` | Secret provider interface |
| `ui/dashboard/src/lib/api.ts` | TypeScript API client (types + endpoints) |
| `ui/dashboard/src/components/layout/AppShell.tsx` | Dashboard layout |

## Coding Standards

### Go

- Standard `gofmt` formatting (enforced by CI)
- No unused imports or variables
- Error messages: lowercase, no period (e.g., `"failed to create provider"`)
- Structured logging with `apilog` or `applog` -- never `fmt.Println` or `log.Println`
- Context passed as first argument
- Follow the plugin pattern (`Register()` in `init()`) for providers
- No hardcoded values -- use config, env vars, or domain pack files

### TypeScript

- ESLint rules from Next.js config
- Functional components with hooks
- Types defined in `src/lib/api.ts` -- keep API types centralized
- CSS custom properties from `src/styles/tokens.css` -- no inline colors or magic numbers

### Documentation

- One sentence per line (better git diffs)
- Code blocks with language tags (```go, ```bash, ```json)
- Include real examples from the codebase, not hypothetical ones
- All code examples must be tested or copied from working code

## Testing Requirements

Every code PR must include tests. We have 350+ tests and don't accept regressions.

### What's Required

| Change Type | Required Tests |
|-------------|---------------|
| New provider | Registration, config validation, factory errors, unit + integration |
| New API endpoint | Handler unit test + integration test with MongoDB |
| New model field | JSON marshal/unmarshal round-trip |
| Agent logic change | Unit test with mock dependencies |
| UI component | Jest test for rendering and user interaction |
| Bug fix | Test that reproduces the bug and verifies the fix |

### Running Tests

```bash
make test-go          # All Go unit tests (no Docker needed)
make test-ui          # Dashboard Jest tests
make lint             # golangci-lint + ESLint
make test-integration # API + MongoDB integration (needs Docker)
make test-k8s         # K8s runner tests with K3s (needs Docker)
make test-secrets     # Secret provider integration (needs Docker)
make test-llm         # LLM integration tests (needs API keys)
```

Integration tests use [testcontainers-go](https://golang.testcontainers.org/) -- Docker is the only dependency. No external services needed.

Install golangci-lint: https://golangci-lint.run/welcome/install/

For the full testing guide including how to write tests and test patterns, see [docs/contributing/testing.md](docs/contributing/testing.md).

## Pull Request Process

### Before Submitting

- [ ] Code builds: `make build`
- [ ] Go tests pass: `make test-go`
- [ ] Lint passes: `make lint`
- [ ] Dashboard tests pass: `make test-ui` (if UI changes)
- [ ] No hardcoded values (use config, env vars, or domain pack files)
- [ ] No secrets or credentials in the code
- [ ] Documentation updated if the change is user-facing
- [ ] Commit messages follow conventional commits format

### Provider PR Checklist

- [ ] Provider registered via `init()` with `RegisterWithMeta()`
- [ ] ConfigFields defined for dashboard form rendering
- [ ] DefaultPricing set (for LLM and warehouse providers)
- [ ] Imported in both `services/agent/main.go` and `services/api/main.go`
- [ ] `replace` directive added in both service `go.mod` files
- [ ] Dockerfile COPY line added for provider `go.mod`/`go.sum`
- [ ] Added to relevant Makefile test targets
- [ ] Unit tests: registration, config validation, factory with missing config
- [ ] Integration tests: skip gracefully without credentials using `t.Skip()`

### Domain Pack PR Checklist

- [ ] `areas.json` with proper structure (id, name, keywords, priority, prompt_file)
- [ ] All prompt files referenced in `areas.json` exist
- [ ] `base_context.md` includes `{{PROFILE}}` and `{{PREVIOUS_CONTEXT}}`
- [ ] Analysis prompts include `{{QUERY_RESULTS}}`
- [ ] Recommendations prompt includes `related_insight_ids` instruction
- [ ] Profile schema is valid JSON Schema
- [ ] Go implementation registered via `init()` with tests
- [ ] Registered in both `services/agent/main.go` and `services/api/main.go`

### Review Process

1. **CI must pass** -- Build, tests, and lint
2. **One maintainer approval** required
3. **No merge conflicts** with `main`
4. PRs are **squash merged** for clean commit history
5. Your branch is automatically deleted after merge

For the full PR guide, see [docs/contributing/pull-requests.md](docs/contributing/pull-requests.md).

## Issue Guidelines

### Issue Types

Each issue has a GitHub issue type:
- **Bug** -- Something isn't working as expected
- **Feature** -- New functionality
- **Task** -- Testing, chores, improvements
- **Enhancement** -- Improvement to existing functionality
- **Docs** -- Documentation changes
- **Chore** -- CI, dependencies, configuration, cleanup

### Labels

Issues are categorized with labels:

| Prefix | Purpose | Examples |
|--------|---------|---------|
| `component:` | Where in the codebase | `component: agent`, `component: api`, `component: dashboard` |
| `component: providers/*` | Which provider type | `component: providers/llm`, `component: providers/warehouse` |
| `priority:` | Urgency level | `priority: P1` (must do) through `priority: P4` (someday) |
| `type:` | Category | `type: feature`, `type: chore`, `type: test`, `type: security` |
| `status:` | Workflow state | `status: blocked`, `status: needs-design`, `status: needs-review` |
| (no prefix) | Special | `good first issue`, `help wanted` |

## Community

- **Questions?** Open a [Discussion](https://github.com/decisionbox-io/decisionbox-platform/discussions) in the Q&A category
- **Ideas?** Start a [Discussion](https://github.com/decisionbox-io/decisionbox-platform/discussions) in the Ideas category
- **Show & Tell** -- Built a domain pack or integration? Share it in [Show and Tell](https://github.com/decisionbox-io/decisionbox-platform/discussions)
- **Bugs & Features** -- Use [Issues](https://github.com/decisionbox-io/decisionbox-platform/issues) with the appropriate template

## Thank You

Every contribution matters -- from fixing a typo to adding a new provider. We appreciate your time and effort in helping make DecisionBox better.
