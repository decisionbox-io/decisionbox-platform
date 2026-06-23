package askserve

import (
	"context"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
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
	// History is the prior conversation, oldest-first, already bounded by the
	// caller. The server applies a secondary newest-first character-budget
	// trim so context stays bounded as a session grows.
	History []HistoryMessage `json:"history,omitempty"`
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

// ProjectRuntime is the public hand-off the dependency builder returns. The
// agentserver package owns the provider/secret wiring (it cannot be imported
// here without an import cycle), so it constructs these per project and this
// package consumes them.
type ProjectRuntime struct {
	// Executor runs SQL with the tenant filter + self-heal already wired.
	Executor *queryexec.QueryExecutor
	// SchemaProvider serves lookup_schema / search_tables. May be nil when the
	// project has no schema index — the loop degrades gracefully.
	SchemaProvider ai.SchemaProvider
	// AIClient drives the reasoning LLM (multi-turn CreateMessage).
	AIClient *ai.Client
	// Model is the project's configured model id (for the transcript record).
	Model string
	// Datasets are the project's configured datasets (rendered into the prompt).
	Datasets []string
	// Dialect is the warehouse SQL dialect name (rendered into the prompt).
	Dialect string
	// FilterField is the tenant-scope column the executor enforces; surfaced
	// in the prompt so the model emits it. Empty for single-tenant datasets.
	FilterField string
	// FilterValue is the tenant-scope value; surfaced in the prompt so the
	// model emits the exact `field = value` predicate. Empty when FilterField
	// is empty.
	FilterValue string
	// Closers release per-project resources (warehouse + schema retriever).
	Closers []func() error
}

// Close releases the per-project resources (warehouse + schema retriever).
func (r *ProjectRuntime) Close() {
	for _, c := range r.Closers {
		if c != nil {
			_ = c()
		}
	}
}

// ProjectBuilder lazily builds the per-project runtime. It must run
// ValidateReadOnly on the warehouse before returning so a turn never executes
// against write-capable credentials. Concurrent calls for the same project
// are serialized by the pool, so the builder need not be reentrancy-safe.
type ProjectBuilder func(ctx context.Context, projectID string) (*ProjectRuntime, error)
