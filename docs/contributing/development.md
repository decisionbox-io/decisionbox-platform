# Development Setup

> **Version**: 0.1.0

This guide covers setting up a local development environment for contributing to DecisionBox.

## Prerequisites

- **Go 1.25+** — [golang.org/dl](https://golang.org/dl/)
- **Node.js 20+** — [nodejs.org](https://nodejs.org/)
- **Docker** — For MongoDB (and integration tests)
- **Make** — Build tool
- **golangci-lint** — [golangci-lint.run/welcome/install](https://golangci-lint.run/welcome/install/) (for `make lint`)

## Clone and Build

```bash
git clone https://github.com/decisionbox-io/decisionbox-platform.git
cd decisionbox-platform

# Build Go binaries
make build

# Install agent binary (API spawns it as subprocess)
sudo cp bin/decisionbox-agent /usr/local/bin/

# Install dashboard dependencies
cd ui/dashboard && npm install && cd ../..
```

## Start Development

```bash
# Start MongoDB
docker compose up -d mongodb

# Terminal 1: API (auto-configures DOMAIN_PACK_PATH)
make dev-api

# Terminal 2: Dashboard (hot-reloads)
make dev-dashboard
```

Open **http://localhost:3000**.

## Project Structure

```
decisionbox-platform/
├── libs/go-common/         # Shared Go interfaces (LLM, warehouse, secrets, health)
├── providers/
│   ├── llm/                # LLM provider implementations
│   ├── warehouse/          # Warehouse provider implementations
│   └── secrets/            # Secret provider implementations
├── domain-packs/gaming/    # Gaming domain pack (prompts, profiles, Go code)
├── services/
│   ├── agent/              # Discovery agent (Go binary)
│   └── api/                # REST API (Go binary)
├── ui/dashboard/           # Web dashboard (Next.js)
├── docs/                   # Documentation (you're reading it)
├── docker-compose.yml      # Local development stack
└── Makefile                # Build, test, dev commands
```

## Go Module Structure

The project uses Go workspaces with local `replace` directives. Each provider and service has its own `go.mod`:

```
libs/go-common/go.mod                    # Shared library
providers/llm/claude/go.mod              # Claude provider
providers/warehouse/bigquery/go.mod      # BigQuery provider
services/agent/go.mod                    # Agent (imports all providers)
services/api/go.mod                      # API (imports all providers)
domain-packs/gaming/go/go.mod           # Gaming domain pack
```

When adding a new provider, create a new `go.mod` and add `replace` directives to the service go.mod files.

## Key Development Patterns

### Adding Functionality

1. **Read existing code** — Understand the patterns before changing them
2. **Follow the plugin pattern** — For new providers, use `Register()` in `init()`
3. **Write tests first** — Unit tests for logic, integration tests for external services
4. **No hardcoded values** — Use config, environment variables, or domain pack files

### Important Files to Know

| File | Purpose |
|------|---------|
| `services/api/internal/server/server.go` | All route registrations |
| `services/agent/internal/discovery/orchestrator.go` | Discovery logic (the brain) |
| `services/agent/agentserver/agentserver.go` | Agent startup, provider initialization (exported `Run()`) |
| `libs/go-common/llm/registry.go` | LLM provider interface + registry |
| `libs/go-common/warehouse/provider.go` | Warehouse provider interface |
| `libs/go-common/secrets/provider.go` | Secret provider interface |
| `ui/dashboard/src/lib/api.ts` | TypeScript API client (all types + endpoints) |
| `ui/dashboard/src/components/layout/AppShell.tsx` | Dashboard layout |

### After Making Changes

```bash
# Rebuild agent if you changed agent code
make build-agent
sudo cp bin/decisionbox-agent /usr/local/bin/

# API hot-reloads (go run .)
# Dashboard hot-reloads (npm run dev)
# Restart API if you changed provider registrations
```

## Environment Variables for Development

The `make dev-api` command sets these automatically:

```bash
MONGODB_URI=mongodb://localhost:27017
MONGODB_DB=decisionbox
DOMAIN_PACK_PATH=../../domain-packs
```

For the agent, set `SECRET_PROVIDER=mongodb` and optionally `SECRET_ENCRYPTION_KEY`.

## Running a Discovery Manually

```bash
# Via API (normal flow)
curl -X POST http://localhost:8080/api/v1/projects/{id}/discover

# Via agent directly (for debugging)
MONGODB_URI=mongodb://localhost:27017 \
MONGODB_DB=decisionbox \
SECRET_PROVIDER=mongodb \
DOMAIN_PACK_PATH=../../domain-packs \
  decisionbox-agent --project-id={id} --max-steps=10
```

## Next Steps

- [Testing](testing.md) — Running and writing tests
- [Pull Requests](pull-requests.md) — Contributing code
