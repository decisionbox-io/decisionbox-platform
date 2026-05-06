# Plugin Hooks

> **Version**: Unreleased

DecisionBox exposes a small set of generic extension points so plugins can add behavior without forking the community code.
Each hook is a leaf-level registry: a plugin imports it (often with a blank import) and calls `Register*` from `init()`; the platform consults the registry at the right moment in its normal flow.

This page documents the three hooks. They are intentionally feature-agnostic so multiple unrelated plugins can attach without naming collisions.

## Hook 1 — Context providers

Plugins can append markdown sections to agent prompts (alongside the project's knowledge sources).
The agent walks every registered provider and concatenates non-empty sections in registration order.

```go
import (
    "context"

    "github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

type myProvider struct{}

func (myProvider) Name() string { return "my-context" }

func (myProvider) Section(ctx context.Context, projectID, query string, opts agentplugin.ContextProviderOpts) (string, error) {
    // Return a markdown section, or "" when there is nothing to add.
    // Honor opts.Limit / opts.MinScore for retrieval-style providers.
    return "## My Context\nThis project belongs to the wholesale tenant.", nil
}

func init() {
    agentplugin.RegisterContextProvider(myProvider{})
}
```

Behavior:

- Multiple registrations are allowed; output is emitted in registration order.
- Empty strings are dropped silently — providers don't have to special-case "nothing to add".
- A panicking or erroring provider does not abort the prompt; its section is skipped and the failure is reported via the `onError` callback the agent passes.
- Calling `RegisterContextProvider` with a duplicate name or a nil provider panics.

The platform's built-in knowledge-sources retriever registers itself through this hook (`libs/go-common/sources/contextprovider.go`).
That registration is the canonical example — community behavior is identical to a single-call site, but the registry lets future plugins (column hints, area priorities, …) attach without orchestrator edits.

## Hook 2 — `ListTables` filter

Plugins can shrink the per-dataset table list the agent discovers, after the warehouse driver returns it and before per-table schema discovery starts.
Use it to implement allow / deny lists, regex denylists, or any policy that maps "tables the warehouse exposes" to "tables this project should consider".

```go
import (
    "context"

    "github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

func init() {
    agentplugin.RegisterListTablesFilter("my-scope", func(ctx context.Context, projectID, dataset string, in []string) ([]string, error) {
        out := in[:0:0]
        for _, t := range in {
            if t != "deprecated_table" {
                out = append(out, t)
            }
        }
        return out, nil
    })
}
```

Rules:

- Filters MUST NOT add tables to the input. The warehouse's exposed list is the upper bound.
- Filters run in registration order. The output of one filter is the input to the next.
- A filter that returns an error fails the dataset; the agent logs and skips it, same as a `ListTablesInDataset` failure.
- Empty names or nil functions panic. Re-registering the same name panics.

## Hook 3 — Ask handler override

Plugins can replace the community handler for `POST /api/v1/projects/{id}/ask` so they can add tool-use loops, agentic flows, or alternative synthesis on top of the platform.

```go
import (
    "net/http"

    "github.com/decisionbox-io/decisionbox/services/api/apiserver"
)

type customAsk struct{}

func (customAsk) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    // ... full agentic flow, tool calls, proposal cards, …
}

func init() {
    apiserver.RegisterAskOverride(customAsk{})
}
```

Behavior:

- The community route reads the override on every request, so plugins can register from `init()` or later.
- With no override registered the community RAG handler runs unchanged.
- Calling `RegisterAskOverride` with `nil` or twice panics — the override is process-global and silent shadowing would be a footgun.
- The override receives the raw `*http.Request` after RBAC middleware (the route is gated at `viewer`); it is responsible for any further role checks (e.g. requiring `member` to mutate state).

## Migration & compatibility

These hooks are additive — plugins that don't care about a given hook simply do not register.
The community code paths invoke the registries unconditionally; an empty registry is a no-op.

A reference consumer for context providers (`libs/go-common/sources`) and a regression guard in the orchestrator (`orchestrator_plugin_hooks_test.go`) keep the prompt output byte-for-byte stable when no plugin is loaded.
