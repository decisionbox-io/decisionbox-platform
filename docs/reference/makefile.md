# Makefile Targets Reference

> **Version**: 0.4.0

All common commands are available via `make`. Run `make help` to see the full list.

## Docker Compose

| Target | Description |
|--------|-------------|
| `make up` | Start all services (MongoDB + API + Dashboard) via Docker Compose |
| `make down` | Stop all services |
| `make reset` | Stop all services and remove volumes (deletes all data) |
| `make logs` | Follow logs from all services |
| `make logs-api` | Follow API logs only |

## Build

| Target | Description |
|--------|-------------|
| `make build` | Build all Go binaries (agent + API) to `bin/` |
| `make build-agent` | Build the agent binary → `bin/decisionbox-agent` |
| `make build-api` | Build the API binary → `bin/decisionbox-api` |
| `make build-dashboard` | Build the dashboard (`npm run build`) |

## Test

| Target | Description | Requirements |
|--------|-------------|--------------|
| `make test` | Run all tests (Go + UI) | Docker for integration tests |
| `make test-go` | Run all Go unit tests across all modules | None |
| `make test-integration` | Run integration tests (MongoDB testcontainer) | Docker |
| `make test-k8s` | Run K8s runner integration tests (K3s testcontainer) | Docker |
| `make test-secrets` | Run secrets provider integration tests | Docker |
| `make test-ollama` | Run Ollama LLM integration tests (slow, downloads model) | Docker |
| `make test-llm` | Run LLM provider integration tests (skips without creds) | API keys (see below) |
| `make test-ui` | Run dashboard unit tests | Node.js |

## Lint

| Target | Description | Requirements |
|--------|-------------|--------------|
| `make lint` | Run all linters (Go + UI) | golangci-lint, Node.js |
| `make lint-go` | Run golangci-lint on all Go modules | [golangci-lint](https://golangci-lint.run/welcome/install/) |
| `make lint-ui` | Run ESLint on dashboard | Node.js |

### LLM Integration Test Environment Variables

```bash
# Set any/all of these to run LLM integration tests:
export INTEGRATION_TEST_ANTHROPIC_API_KEY=sk-ant-...    # Claude
export INTEGRATION_TEST_OPENAI_API_KEY=sk-...           # OpenAI
export INTEGRATION_TEST_VERTEX_PROJECT_ID=my-gcp-proj   # Vertex AI (+ GCP ADC)
export INTEGRATION_TEST_BEDROCK_REGION=us-east-1        # Bedrock (+ AWS creds)

make test-llm
```

Tests skip gracefully when credentials are not set.

## Development

| Target | Description |
|--------|-------------|
| `make dev-api` | Run API locally with `go run .` (requires MongoDB on localhost:27017) |
| `make dev-dashboard` | Run dashboard locally with `npm run dev` |
| `make agent-run PROJECT_ID=xxx` | Run the agent directly for a project |

### Typical Development Workflow

```bash
# Start MongoDB
docker compose up -d mongodb

# Terminal 1: API
make dev-api

# Terminal 2: Dashboard
make dev-dashboard

# Open http://localhost:3000
```

## Docker Images

| Target | Description |
|--------|-------------|
| `make docker-build` | Build all 3 Docker images |
| `make docker-build-api` | Build API image |
| `make docker-build-agent` | Build Agent image |
| `make docker-build-dashboard` | Build Dashboard image |
| `make docker-push` | Push all images to registry |

### Custom Registry and Tags

```bash
make docker-build REGISTRY=my-registry.com/myorg TAG=v0.2.0
make docker-push REGISTRY=my-registry.com/myorg TAG=v0.2.0
```

Defaults: `REGISTRY=ghcr.io/decisionbox-io`, `TAG=latest`.

## Clean

| Target | Description |
|--------|-------------|
| `make clean` | Remove build artifacts (`bin/`, `ui/dashboard/.next`) |

## Next Steps

- [Development Setup](../contributing/development.md) — Full local dev environment
- [Testing Guide](../contributing/testing.md) — Writing and running tests
