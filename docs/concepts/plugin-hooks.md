# Plugin Hooks

> **Version**: Unreleased

DecisionBox exposes a small set of generic extension points so plugins can add behavior without forking the community code.
Each hook is a leaf-level registry: a plugin imports it (often with a blank import) and calls `Register*` from `init()`; the platform consults the registry at the right moment in its normal flow.

This page documents the five hooks. They are intentionally feature-agnostic so multiple unrelated plugins can attach without naming collisions.

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

## Hook 4 — Cached-schema filter

Plugins can shrink the catalog the agent loads from `project_schema_cache` at the start of a discovery run.
Different trigger point from Hook 2: `ListTablesFilter` runs during a fresh schema-index pass (constraining what gets cached); `CachedSchemaFilter` runs every discovery run after the cache load (constraining what the LLM sees this run).
Together they let an allow-/deny-list save take effect for both already-indexed and freshly-indexed projects without re-indexing.

```go
import (
    "context"

    "github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
)

func init() {
    agentplugin.RegisterCachedSchemaFilter("my-scope", func(ctx context.Context, projectID string, qualified []string) ([]string, error) {
        out := qualified[:0:0]
        for _, t := range qualified {
            if t != "demo.deprecated" {
                out = append(out, t)
            }
        }
        return out, nil
    })
}
```

Rules:

- Input is a slice of qualified table names (`<dataset>.<table>`, `<schema>.<table>`, etc. — whatever shape the warehouse provider canonicalised on when it wrote the schema cache). Sorted ascending so filter behavior and downstream logs are deterministic across runs.
- Filters MUST NOT add tables. The orchestrator validates the output is a subset of the input and aborts the run with an explicit error if a filter invents a key, so a misbehaving plugin can't surface phantom tables to the LLM.
- A filter that drops every table aborts the run with a "review the discovery scope" error rather than letting an empty catalog hit the LLM.
- A non-nil error from any filter aborts the run; the chain stops on the first error and the orchestrator surfaces the wrapped reason.
- Empty names or nil functions panic. Re-registering the same name panics.

## Hook 5 — Discovery run completion

Plugins can react to every discovery run that reaches a terminal state (`completed`, `failed`, or `cancelled`).
Use it to enqueue a downstream side effect — generate an executive summary, post a Slack notification, write an audit record — without patching the agent or the run-completion path.

```go
import (
    "context"

    "github.com/decisionbox-io/decisionbox/services/api/apiserver"
)

func init() {
    apiserver.RegisterRunCompletionHook("exec-summary", func(ctx context.Context, run apiserver.RunCompletion) error {
        if run.Status != "completed" {
            return nil
        }
        // Enqueue executive-summary generation for the discovery.
        return enqueueExecSummary(ctx, run.RunID, run.ProjectID)
    })
}
```

Behavior:

- Each registered hook is named; registering with an empty name, a `nil` function, or a duplicate name panics — silent shadowing in a process-global registry would be a footgun.
- The API spins up a 15-second-tick background dispatcher only when at least one hook is registered. The dispatcher scans for runs in a terminal state (`completed` / `failed` / `cancelled`) whose `completion_hooks_fired_at` field is unset, fires every hook in registration order, and stamps the field once every hook returns `nil`.
- A non-nil return from any hook leaves the run unmarked so every hook re-fires on the next tick. Hooks MUST therefore be idempotent — a peer hook failing on the same run will cause successful peers to be invoked again.
- A hook that panics is recovered; its result records a panic-tagged error and subsequent hooks still run.
- Hook execution is sequential within one run so an upstream hook (e.g. an audit record) finishes before a downstream one (e.g. a Slack notification) observes its side effect.
- The `RunCompletion` payload carries `RunID`, `ProjectID`, `Status`, `CompletedAt`, and `Error` (set only for `failed` runs). The agent persists insights and recommendations before flipping the run to `completed`, so consumers may rely on those collections being readable when the hook fires for a successful run.

## Migration & compatibility

These hooks are additive — plugins that don't care about a given hook simply do not register.
The community code paths invoke the registries unconditionally; an empty registry is a no-op.

A reference consumer for context providers (`libs/go-common/sources`) and a regression guard in the orchestrator (`orchestrator_plugin_hooks_test.go`) keep the prompt output byte-for-byte stable when no plugin is loaded.
