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
}

// Result is what a mutation tool returns. A tool that produced an approvable
// proposal sets ProposalID so the loop can stamp it on the transcript's
// ToolEvent; Output is the payload fed back to the model.
type Result struct {
	ProposalID string
	Output     any
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
