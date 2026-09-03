package askserve

import (
	"context"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/queryexec"
)

// TurnRequest is the POST /turns body. The caller (the API) has already
// created the AskTurn record in Mongo (status=running) under TurnID and
// assembled a bounded, ordered conversation History; this service runs the
// turn in the background and persists its tool events + final answer.
//
// The wire contract is documented in docs (the API and this server live in
// different modules and share no Go type) — keep the JSON tags stable.
type TurnRequest struct {
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Question  string `json:"question"`
	// DatasourceID pins the whole turn to one datasource (warehouse). When set
	// it is the user's explicit override: every query runs against that
	// datasource and the model is not offered a datasource choice. Empty means
	// "let the model decide" — on a multi-datasource project the model may
	// target any datasource per query (defaulting to the primary) and chain
	// across them in one turn; on a single-datasource project it is ignored.
	DatasourceID string `json:"datasource_id,omitempty"`
	// History is the prior conversation, oldest-first, already bounded by the
	// caller. The server applies a secondary newest-first character-budget
	// trim so context stays bounded as a session grows.
	History []HistoryMessage `json:"history,omitempty"`
	// EnableCharts is the caller's per-turn capability grant for the render_chart
	// tool. It is the entitlement gate: charts are offered only when the caller
	// sets this AND the server's ASK_SERVE_CHARTS_ENABLED kill-switch is on. The
	// community API never sets it (it never calls ask-serve); the enterprise
	// delegate sets it only for entitled deployments. A policy check in-agent is
	// not sufficient on its own (a checker-less agent's NoopChecker allows
	// everything), so the capability rides the wire.
	EnableCharts bool `json:"enable_charts,omitempty"`
	// SeedContext, when set, anchors the whole turn on one insight or
	// recommendation the user launched Ask from ("Ask about this"). The server
	// renders it as a FOCUS block in the system prompt so the answer stays about
	// that entity. The caller resolves + bounds the text; the server never
	// re-fetches. Hand-synced by JSON tag with the enterprise
	// startTurnRequest.SeedContext (the two live in different modules).
	SeedContext *SeedContext `json:"seed_context,omitempty"`
}

// SeedContext is the insight / recommendation a seeded Ask conversation is
// anchored to. Type + ID identify the entity; Label + Text are the resolved,
// bounded values the caller hydrated. Mirrors go-common AskSessionSeed and the
// enterprise wire struct field-for-field.
type SeedContext struct {
	Type  string `json:"type"` // "insight" | "recommendation"
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	Text  string `json:"text,omitempty"`
}

// HistoryMessage is one prior conversation message. Role is "user" or
// "assistant"; assistant turns may fold a compact tool summary into Content
// so the model does not re-run identical queries.
type HistoryMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// TurnResponse is the synchronous ACK for POST /turns — the turn runs in the
// background; progress is read from the durable event log by cursor.
type TurnResponse struct {
	TurnID string `json:"turn_id"`
	Status string `json:"status"`
}

// DatasourceInfo is the routing-facing descriptor of one warehouse: enough for
// the prompt to name it and for the model to target it by id, with no live
// connection. Built from project config at runtime-build time, so a slow or
// unreachable datasource costs nothing until a query actually touches it.
type DatasourceInfo struct {
	// ID is the stable datasource id the model names in query_data /
	// lookup_schema ("default" for a legacy/single-warehouse project).
	ID string
	// Label is the human-readable name rendered in the prompt.
	Label string
	// Description is the warehouse-card headline (what this datasource holds) —
	// the primary signal the model uses to pick a datasource.
	Description string
	// Dialect is a dialect hint for the prompt (the warehouse provider type,
	// e.g. "bigquery"). The authoritative dialect + SQL-fix prompt bind at
	// query time from the live connection; this is only so the model writes
	// close-to-correct SQL up front.
	Dialect string
	// Datasets are the datasource's configured datasets, rendered into the
	// prompt so the model qualifies tables correctly.
	Datasets []string
	// FilterField / FilterValue carry the tenant scope for a multi-tenant
	// datasource; surfaced so the model emits the exact predicate. Empty for
	// single-tenant datasources.
	FilterField string
	FilterValue string
	// Card is the structured routing card (subject areas / entities / metrics).
	// Optional; nil when the datasource has no card yet.
	Card *DatasourceCard
}

// DatasourceCard mirrors models.WarehouseCard — the structured routing signal
// rendered into the multi-datasource prompt. Duplicated as a local value type
// so this package does not depend on the agent's project model.
type DatasourceCard struct {
	SubjectAreas []string
	KeyEntities  []string
	KeyMetrics   []string
}

// WarehouseConn is the per-datasource execution context the agentserver builds
// on demand: a self-healing query executor over a warm read-only warehouse
// connection, plus the closers that release it. The ProjectRuntime caches these
// LRU-bounded so a turn that only touches one datasource never opens the
// others, and one slow/broken datasource never blocks a turn that avoids it.
type WarehouseConn struct {
	// Executor runs SQL with the datasource's tenant filter + self-heal wired.
	Executor *queryexec.QueryExecutor
	// Closers release the warehouse connection (and any per-connection
	// resources). Invoked when the entry is evicted or the runtime closes.
	Closers []func() error
}

// WarehouseBuilder lazily builds one datasource's execution context. It MUST
// run ValidateReadOnly on the warehouse before returning so a turn never
// executes against write-capable credentials. Concurrent builds for the same
// datasource are serialized by the runtime, so it need not be reentrancy-safe.
type WarehouseBuilder func(ctx context.Context, warehouseID string) (*WarehouseConn, error)

// ProjectBuilder lazily builds the per-project runtime (shared LLM + insights +
// the eager schema router + the lazy per-datasource warehouse builder).
// Concurrent calls for the same project are serialized by the pool, so the
// builder need not be reentrancy-safe.
type ProjectBuilder func(ctx context.Context, projectID string) (*ProjectRuntime, error)
