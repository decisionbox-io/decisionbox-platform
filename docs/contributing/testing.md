# Testing

> **Version**: 0.1.0

DecisionBox has 350+ tests across unit and integration suites. This guide covers running tests and writing new ones.

## Running Tests

### All Tests

```bash
make test          # All Go + UI tests
make test-go       # All Go unit tests only
make lint          # All linters (golangci-lint + ESLint)
```

### By Category

| Command | What It Tests | Needs |
|---------|--------------|-------|
| `make test-go` | All Go unit tests across all modules | Nothing |
| `make test-ui` | Dashboard unit tests (Jest) | Node.js |
| `make lint-go` | Go linting via golangci-lint | [golangci-lint](https://golangci-lint.run/welcome/install/) |
| `make lint-ui` | Dashboard linting via ESLint | Node.js |
| `make test-integration` | MongoDB + API integration tests | Docker |
| `make test-k8s` | K8s runner tests (K3s testcontainer) | Docker |
| `make test-secrets` | Secret provider integration tests | Docker |
| `make test-ollama` | Ollama LLM integration tests (slow) | Docker |
| `make test-llm` | LLM provider integration tests | API keys |

### CI Pipeline

The CI workflow runs automatically on every PR and push to main:

| Job | What It Does | Trigger |
|-----|-------------|---------|
| Go Build | Compiles API + Agent binaries | Go files changed |
| Go Test | All Go unit tests | Go files changed |
| Go Lint | golangci-lint on all modules | Go files changed |
| Dashboard Build | Next.js build | Dashboard files changed |
| Dashboard Test & Lint | Jest + ESLint | Dashboard files changed |
| License Check | SBOM generation (Syft) + license policy validation (Grant) | Go or Dashboard files changed |
| Integration Tests | MongoDB integration tests | Push to main, or PR with `run-integration-tests` label |

To trigger integration tests on a PR, add the `run-integration-tests` label.

### LLM Integration Tests

These test against real LLM APIs. They skip gracefully when credentials are not set:

```bash
export INTEGRATION_TEST_ANTHROPIC_API_KEY=sk-ant-...
export INTEGRATION_TEST_OPENAI_API_KEY=sk-...
export INTEGRATION_TEST_VERTEX_PROJECT_ID=my-gcp-project
export INTEGRATION_TEST_BEDROCK_REGION=us-east-1

make test-llm
```

## Test Structure

### Go Tests

Tests live alongside the code they test:

```
services/agent/
├── internal/
│   ├── models/
│   │   ├── discovery.go           # Code
│   │   └── discovery_test.go      # Unit tests
│   ├── discovery/
│   │   ├── orchestrator.go
│   │   ├── selective_test.go      # Unit tests
│   │   └── integration_test.go    # Integration tests (//go:build integration)
```

### Integration Tests

Integration tests use build tags:

```go
//go:build integration

package discovery
```

They're excluded from `go test ./...` and only run with `-tags=integration`.

### Testcontainers

Integration tests use [testcontainers-go](https://golang.testcontainers.org/) to spin up dependencies:

| Dependency | Testcontainer | Used By |
|-----------|---------------|---------|
| MongoDB | `testcontainers-go/modules/mongodb` | Agent + API integration tests |
| K3s | `testcontainers-go/modules/k3s` | K8s runner tests |
| Ollama | `testcontainers-go/modules/ollama` | Ollama LLM tests |

### Dashboard Tests

```bash
cd ui/dashboard
npm test              # Run once
npm run test:watch    # Watch mode
npm run test:coverage # Coverage report
```

Uses Jest + React Testing Library. Test files: `*.test.ts` or `*.test.tsx`.

## Writing Tests

### Unit Test Example

```go
// providers/llm/myprovider/provider_test.go
package myprovider

import (
    "testing"

    gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestRegistered(t *testing.T) {
    _, ok := gollm.GetProviderMeta("myprovider")
    if !ok {
        t.Fatal("myprovider not registered")
    }
}

func TestFactoryMissingKey(t *testing.T) {
    _, err := gollm.NewProvider("myprovider", gollm.ProviderConfig{})
    if err == nil {
        t.Fatal("should error without API key")
    }
}
```

### Integration Test Example

```go
//go:build integration

package myprovider

import (
    "context"
    "os"
    "testing"
    "time"

    gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
)

func TestIntegration_BasicChat(t *testing.T) {
    apiKey := os.Getenv("INTEGRATION_TEST_MYPROVIDER_API_KEY")
    if apiKey == "" {
        t.Skip("INTEGRATION_TEST_MYPROVIDER_API_KEY not set")
    }

    provider, _ := gollm.NewProvider("myprovider", gollm.ProviderConfig{
        "api_key": apiKey,
    })

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    resp, err := provider.Chat(ctx, gollm.ChatRequest{
        Messages:  []gollm.Message{{Role: "user", Content: "Say hello."}},
        MaxTokens: 10,
    })
    if err != nil {
        t.Fatalf("Chat error: %v", err)
    }
    if resp.Content == "" {
        t.Error("empty response")
    }
}
```

### Test Quality Standards

- **One thing per test** — Each test verifies one behavior
- **Descriptive names** — `TestRecommendationJSON_WithRelatedInsights`, not `TestFunc1`
- **No external dependencies** — Unit tests don't need Docker, APIs, or credentials
- **Skip gracefully** — Integration tests use `t.Skip()` when credentials are missing
- **Fast** — Unit tests should complete in < 100ms
- **Deterministic** — No random failures

### What to Test

When adding a new feature:

| Change | Required Tests |
|--------|---------------|
| New provider | Registration, config validation, factory errors, interface compliance |
| New API endpoint | Handler unit test, integration test with MongoDB |
| New model field | JSON marshal/unmarshal round-trip |
| New agent logic | Unit test with mock dependencies |
| UI component | Jest test for rendering and behavior |

## Test Coverage

Run coverage for a specific package:

```bash
cd services/agent/internal/models
go test -cover ./...

# HTML report
go test -coverprofile=cover.out ./...
go tool cover -html=cover.out
```

## Next Steps

- [Development Setup](development.md) — Local environment
- [Pull Requests](pull-requests.md) — Contributing code
