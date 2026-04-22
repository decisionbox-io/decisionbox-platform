# Adding LLM Providers

> **Version**: 0.4.0

There are two kinds of "adding": adding a **new model** to an existing cloud, which is one line in the catalog; and adding a **new cloud**, which is a full Go package. Pick the right one.

## Adding a new model to an existing cloud

If the new model speaks a wire format the cloud provider already implements (Anthropic, OpenAI-compat, or Google-native), you do not touch provider code at all.

Add one `Register` call in `libs/go-common/llm/modelcatalog/catalog.go`:

```go
// Bedrock newly released a model that speaks the OpenAI chat-completions wire.
Register(Entry{
    Cloud:                 "bedrock",
    ID:                    "vendor.new-model-2027-v1:0",
    Wire:                  OpenAICompat,
    DisplayName:           "New Model 2027 (Bedrock)",
    MaxOutputTokens:       32768,
    InputPricePerMillion:  0.50,
    OutputPricePerMillion: 1.50,
})
```

Then add a test in `registry_test.go` to pin the row, and you are done — the dashboard picks it up automatically.

If you do not want to wait for the catalog to be updated, users can set `llm.config.wire_override` on their project to route uncatalogued models at their own risk. See [Configuring LLM Providers](configuring-llm.md#wire_override--for-uncatalogued-models).

## Adding a new wire format

If the new cloud speaks a wire that no existing provider implements (say, a native Cohere wire or AI21), add a value to the `Wire` enum in `libs/go-common/llm/modelcatalog/registry.go` (including `Valid()`, `ParseWire()`) and implement a handler in every provider that will host models on that wire. This is rare.

## Adding a whole new cloud

This guide shows the common case: a new cloud that speaks an OpenAI-compatible wire (most do today). For a non-compatible wire you follow the same skeleton but build the request/response by hand.

## Interface

```go
// libs/go-common/llm/provider.go
type Provider interface {
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    Validate(ctx context.Context) error
}
```

`Validate` checks that credentials and configuration are valid without consuming tokens.
Use lightweight API calls (e.g., list models) when possible.
Called by the "Test Connection" button in the dashboard.

**ChatRequest:**

| Field | Type | Description |
|-------|------|-------------|
| `Model` | string | Model ID (may be overridden per-request) |
| `SystemPrompt` | string | System-level instruction |
| `Messages` | []Message | Conversation messages (`{Role, Content}`) |
| `MaxTokens` | int | Maximum output tokens |
| `Temperature` | float64 | Sampling temperature (0.0–1.0) |

**ChatResponse:**

| Field | Type | Description |
|-------|------|-------------|
| `Content` | string | Response text |
| `Model` | string | Model that generated this |
| `StopReason` | string | Why generation stopped |
| `Usage.InputTokens` | int | Input tokens consumed |
| `Usage.OutputTokens` | int | Output tokens generated |

## Step 1: Create the Package

```bash
mkdir -p providers/llm/myprovider
cd providers/llm/myprovider
go mod init github.com/decisionbox-io/decisionbox/providers/llm/myprovider
```

Add to `go.mod`:
```
require github.com/decisionbox-io/decisionbox/libs/go-common v0.0.0
replace github.com/decisionbox-io/decisionbox/libs/go-common => ../../../libs/go-common
```

## Step 2: Implement the Provider

Below is the skeleton for an **OpenAI-compatible** cloud. Your Chat() is almost entirely delegation — the `openaicompat` package handles request body, response parse, and typed error extraction.

```go
// providers/llm/myprovider/provider.go
package myprovider

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "strconv"
    "time"

    gollm "github.com/decisionbox-io/decisionbox/libs/go-common/llm"
    "github.com/decisionbox-io/decisionbox/libs/go-common/llm/modelcatalog"
    "github.com/decisionbox-io/decisionbox/libs/go-common/llm/openaicompat"
)

func init() {
    gollm.RegisterWithMeta("myprovider", func(cfg gollm.ProviderConfig) (gollm.Provider, error) {
        apiKey := cfg["api_key"]
        if apiKey == "" {
            return nil, fmt.Errorf("myprovider: api_key is required")
        }
        model := cfg["model"]
        if model == "" {
            return nil, fmt.Errorf("myprovider: model is required")
        }

        // If the provider hosts models of different wires, parse wire_override here.
        wireOverride := modelcatalog.Unknown
        if raw := cfg["wire_override"]; raw != "" {
            parsed := modelcatalog.ParseWire(raw)
            if !parsed.Valid() {
                return nil, fmt.Errorf("myprovider: invalid wire_override %q", raw)
            }
            wireOverride = parsed
        }

        timeoutSec, _ := strconv.Atoi(cfg["timeout_seconds"])
        if timeoutSec == 0 {
            timeoutSec = 300
        }

        return &MyProvider{
            apiKey:       apiKey,
            model:        model,
            wireOverride: wireOverride,
            httpClient:   &http.Client{Timeout: time.Duration(timeoutSec) * time.Second},
        }, nil
    }, gollm.ProviderMeta{
        Name:        "My LLM Provider",
        Description: "Description shown in the dashboard",
        ConfigFields: []gollm.ConfigField{
            {Key: "api_key", Label: "API Key", Required: true, Type: "string", Placeholder: "your-key-here"},
            {Key: "model", Label: "Model", Required: true, Type: "string"},
            {Key: "wire_override", Label: "Wire override", Type: "string", Description: "Only for models not in the catalog."},
        },
    })
}

type MyProvider struct {
    apiKey       string
    model        string
    wireOverride modelcatalog.Wire
    httpClient   *http.Client
}

func (p *MyProvider) Validate(ctx context.Context) error {
    _, err := p.Chat(ctx, gollm.ChatRequest{
        Model: p.model, Messages: []gollm.Message{{Role: "user", Content: "hi"}}, MaxTokens: 1,
    })
    return err
}

func (p *MyProvider) Chat(ctx context.Context, req gollm.ChatRequest) (*gollm.ChatResponse, error) {
    if req.Model == "" {
        req.Model = p.model
    }

    // If you support multiple wires, resolve and dispatch:
    wire, err := modelcatalog.ResolveWire("myprovider", req.Model, p.wireOverride)
    if err != nil {
        return nil, err
    }
    if wire != modelcatalog.OpenAICompat {
        return nil, fmt.Errorf("myprovider: wire %q not implemented", wire)
    }

    body := openaicompat.BuildRequestBody(req.Model, req)
    buf, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("myprovider: marshal request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, "POST", "https://api.myprovider.com/v1/chat/completions", bytes.NewReader(buf))
    if err != nil {
        return nil, fmt.Errorf("myprovider: build request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

    httpResp, err := p.httpClient.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("myprovider: request failed: %w", err)
    }
    defer httpResp.Body.Close()

    raw, err := io.ReadAll(httpResp.Body)
    if err != nil {
        return nil, fmt.Errorf("myprovider: read response: %w", err)
    }

    if httpResp.StatusCode != http.StatusOK {
        if apiErr := openaicompat.ExtractAPIError(raw); apiErr != nil {
            return nil, fmt.Errorf("myprovider: API error (%d): %s - %s", httpResp.StatusCode, apiErr.Type, apiErr.Message)
        }
        return nil, fmt.Errorf("myprovider: API error (%d): %s", httpResp.StatusCode, string(raw))
    }
    return openaicompat.ParseResponseBody(raw)
}
```

### Key Implementation Notes

- **Read `timeout_seconds` from config** — The agent passes this from the `LLM_TIMEOUT` env var.
- **Support model override** — `req.Model` may differ from the provider default (per-request override).
- **Return accurate token counts** — Used for cost estimation and context tracking. `openaicompat.ParseResponseBody` fills them from the server's `usage` object.
- **Handle retries externally** — The agent's AI client handles retries. Your provider should not retry internally.
- **Register models in the catalog** — For every model the provider supports, add a row in `libs/go-common/llm/modelcatalog/catalog.go` with `Wire`, `MaxOutputTokens` and pricing. The agent uses `MaxOutputTokens` to cap completions during phases that need long output (recommendation generation).

## Step 3: Register in Services

Add blank imports to both services:

```go
// services/agent/agentserver/agentserver.go
import _ "github.com/decisionbox-io/decisionbox/providers/llm/myprovider"

// services/api/apiserver/apiserver.go
import _ "github.com/decisionbox-io/decisionbox/providers/llm/myprovider"
```

Add `replace` directives to both `services/agent/go.mod` and `services/api/go.mod`:

```
require github.com/decisionbox-io/decisionbox/providers/llm/myprovider v0.0.0
replace github.com/decisionbox-io/decisionbox/providers/llm/myprovider => ../../providers/llm/myprovider
```

Update Dockerfiles to copy the go.mod (and go.sum if needed):

```dockerfile
# In services/agent/Dockerfile and services/api/Dockerfile
COPY providers/llm/myprovider/go.mod providers/llm/myprovider/
```

## Step 4: Write Tests

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

func TestFactorySuccess(t *testing.T) {
    _, err := gollm.NewProvider("myprovider", gollm.ProviderConfig{
        "api_key": "test-key",
        "model":   "test-model",
    })
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}
```

Add integration tests that skip without credentials:

```go
// providers/llm/myprovider/integration_test.go
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
        "api_key": apiKey, "model": "default-model",
    })

    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()

    resp, err := provider.Chat(ctx, gollm.ChatRequest{
        Messages:  []gollm.Message{{Role: "user", Content: "Say hello in one word."}},
        MaxTokens: 10,
    })
    if err != nil {
        t.Fatalf("Chat error: %v", err)
    }
    if resp.Content == "" {
        t.Error("empty response")
    }
    t.Logf("Response: %q (tokens: in=%d out=%d)", resp.Content, resp.Usage.InputTokens, resp.Usage.OutputTokens)
}
```

## Step 5: Add to Makefile

Add your provider to the test targets:

```makefile
# In test-go target:
cd providers/llm/myprovider && go test ./...

# In test-llm target (integration):
cd providers/llm/myprovider && go test -tags=integration -count=1 -timeout=2m -v ./...
```

## Checklist

- [ ] `init()` registers with `RegisterWithMeta` (name, factory, metadata)
- [ ] `ConfigFields` includes all user-configurable fields (including `wire_override` if multi-wire)
- [ ] Every supported model is registered in `libs/go-common/llm/modelcatalog/catalog.go` with Wire and MaxOutputTokens
- [ ] `timeout_seconds` read from config (not hardcoded)
- [ ] Model override supported (`req.Model` takes priority)
- [ ] Token usage returned accurately (via `openaicompat.ParseResponseBody` if OpenAI-compat)
- [ ] Imported in agent + API (blank imports in `agentserver.go` and `apiserver.go`)
- [ ] `replace` directive in both go.mod files
- [ ] Dockerfile COPY line for go.mod
- [ ] Unit tests (registration, factory, config validation, wire dispatch)
- [ ] Integration tests (skip without credentials)
- [ ] Added to Makefile test targets

## Next Steps

- [Providers Concept](../concepts/providers.md) — Plugin architecture overview
- [Configuring LLM Providers](configuring-llm.md) — How users set up LLM providers
