// Package askmutation is the registry through which enterprise plugins expose
// mutation tools (today: save a note) to the agentic-Ask data-query loop
// (services/agent/internal/askserve). The loop is a read-only reasoning engine
// in the community platform; this registry is the single, generic extension
// point that lets a plugin add a write action without the loop knowing about
// any particular feature.
//
// The community build registers nothing, so the loop offers no mutation tools
// and its behaviour is unchanged. The enterprise agent binary blank-imports a
// plugin whose init() calls RegisterTool; because ask-serve runs in that binary,
// the tool is then offered — the same "present ⇒ offered" wiring search_insights
// and the knowledge provider already use.
package askmutation

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"go.mongodb.org/mongo-driver/mongo"
)

// QueryRequest names one result to re-read: the datasource it was observed
// against, the query that produced it, and the most rows the caller can use.
type QueryRequest struct {
	// DatasourceID is required. A re-read addresses a specific observation, so
	// the caller knows which of the project's warehouses produced it; guessing
	// on its behalf could read the wrong data, which is the worst outcome here.
	DatasourceID string
	// SQL is the query to run, verbatim. It is never parsed or rewritten.
	SQL string
	// MaxRows bounds the rows returned. 0 means the host's own fetch cap, which
	// bounds it in either case.
	MaxRows int
}

// QualityCaveat is one thing the SOURCE said about a result's fidelity: rows
// it withheld, values it sampled, a cap the query reached.
//
// Re-declared here rather than shared with the warehouse package, which this
// registry does not depend on. Kind is the source's own machine-readable
// label; Detail is its own words, which are the ones worth showing.
type QualityCaveat struct {
	Kind   string
	Detail string
}

// QueryResult is a bounded read.
type QueryResult struct {
	Columns []string
	Rows    []map[string]any
	// RowCount is what the source returned BEFORE MaxRows was applied, so a
	// caller can say "showing N of M" truthfully.
	RowCount int
	// Quality is what the source declared about this result. A caller that
	// presents rows as a population MUST check it: a sampled or capped result
	// comes back with len(Rows) == RowCount and is not the whole answer.
	Quality []QualityCaveat
}

// QueryFunc re-runs a query the turn has already run: read-only, inside the
// project, and bounded. The host binds it (the ask-serve loop owns the
// warehouse connections); it is nil when the host offers no re-read, so a tool
// MUST handle nil rather than assume one.
//
// The query runs exactly as given. It is not repaired on failure, so a caller
// can never be handed rows from a rewritten query under the original query's
// name, and nothing here parses or edits the SQL — a row cap is applied to the
// rows returned, not by splicing a LIMIT the source's dialect may not use.
//
// An unknown datasource, a query the tenant filter refuses, a timeout and a
// warehouse error are all errors. A caller that cannot proceed without the rows
// should say so to its user rather than present a smaller answer as complete.
type QueryFunc func(ctx context.Context, q QueryRequest) (*QueryResult, error)

// Request is the per-call context a mutation tool receives. The agent binds the
// shared Mongo handle (the loop itself stays storage-agnostic); the rest is the
// turn identity the tool stamps on whatever it persists.
type Request struct {
	// ProjectID scopes the mutation. Tools MUST refuse to write outside it.
	ProjectID string
	// SessionID / TurnID identify the Ask conversation + turn, for correlating
	// the resulting proposal back to the transcript.
	SessionID string
	TurnID    string
	// CallerSub is the auth subject of the user whose Ask call produced this,
	// recorded for audit. Empty under NoAuth — not an authorization input.
	CallerSub string
	// Args is the tool input as the model emitted it.
	Args map[string]any
	// Mongo is the shared platform database handle the agent binds at build time.
	Mongo *mongo.Database
	// Query re-reads a result this turn observed, for a tool that needs more of
	// it than the turn's own bounded preview carries. Nil when the host offers
	// no re-read — handle that, never assume it is set.
	Query QueryFunc
}

// Result is what a mutation tool returns. A tool that produced an approvable
// proposal sets ProposalID so the loop can stamp it on the transcript's
// ToolEvent; Output is the payload fed back to the model.
type Result struct {
	ProposalID string
	Output     any
	// Ack is the sentence the loop uses when this tool's outcome ends a turn
	// that gathered no query evidence. The loop replaces the model's prose in
	// that case (an ungrounded answer could carry invented figures) and
	// otherwise falls back to a generic acknowledgement — which can only
	// describe a proposal, because the loop has no idea what a plugin's tool
	// actually did. A tool whose outcome is something else entirely should say
	// so here.
	//
	// It is PLUGIN text, never model text: supply a fixed sentence, never
	// anything derived from the model's arguments, or the safety property the
	// deterministic acknowledgement exists for is lost. Optional.
	Ack string
}

// Executor runs one mutation invocation. It MUST honour ctx and return a
// non-nil error on failure (the loop surfaces it to the model as a tool error so
// it can self-correct or explain).
type Executor func(ctx context.Context, req Request) (Result, error)

// Tool is one registered mutation tool.
type Tool struct {
	// Name is the wire-level tool identifier the model emits (e.g. "save_note").
	Name string
	// Description is the natural-language description the model reads.
	Description string
	// InputSchema is a JSON Schema object describing the tool's input.
	InputSchema map[string]any
	// Run executes the tool.
	Run Executor
}

var (
	mu       sync.RWMutex
	registry = map[string]Tool{}
)

// RegisterTool adds a mutation tool. Plugins call this from init(). Empty name,
// nil Run, or a duplicate name panics — these are programmer errors at startup.
func RegisterTool(t Tool) {
	if t.Name == "" {
		panic("askmutation: RegisterTool called with empty name")
	}
	if t.Run == nil {
		panic(fmt.Sprintf("askmutation: RegisterTool %q called with nil Run", t.Name))
	}
	mu.Lock()
	defer mu.Unlock()
	if _, exists := registry[t.Name]; exists {
		panic(fmt.Sprintf("askmutation: tool %q already registered", t.Name))
	}
	registry[t.Name] = t
}

// Tools returns the registered mutation tools in deterministic name order (so a
// provider's prompt-cache stays warm across requests).
func Tools() []Tool {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Tool, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}

// ResetForTest clears the registry. Test-only; production code MUST NOT call it.
func ResetForTest() {
	mu.Lock()
	defer mu.Unlock()
	registry = map[string]Tool{}
}
