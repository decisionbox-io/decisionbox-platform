package agentplugin

import (
	"context"
	"fmt"
	"sync"
)

// Correlating two datasources means hopping: read a bounded set of values out
// of one, then filter the other by them. Which field binds the two is a choice
// the exploration agent makes from names alone, and two systems can agree on a
// name and disagree on everything else.
//
// Somebody may already have looked at that question and written an answer down
// — this field really does identify the same thing on both sides, this one was
// checked and does not. That knowledge is produced outside this repository, so
// this registry is the seam it plugs into: with a provider wired, the agent can
// ask what has been decided about a pair before it commits to a hop; with none
// wired, the answer is an empty one and the agent explores exactly as it did
// before.
//
// Read-only by construction. Nothing here records a decision, and a provider
// that cannot be reached is never a reason to fail a run.

// CorrelationState is what somebody decided about one pairing.
//
// Three states rather than a boolean, because they rest on different evidence
// and the agent should treat them differently. Confirmed and declared both say
// "use this"; they differ in whether anything proposed the pairing before a
// person ratified it. Rejected is the one that changes what the agent must NOT
// do, and it is the reason this seam exists at all.
type CorrelationState string

const (
	// CorrelationConfirmed: something proposed this pairing and a person
	// checked it.
	CorrelationConfirmed CorrelationState = "confirmed"
	// CorrelationRejected: a person checked this pairing and says the two
	// sides do not hold the same values.
	CorrelationRejected CorrelationState = "rejected"
	// CorrelationManual: a person named this pairing outright; nothing
	// proposed it.
	CorrelationManual CorrelationState = "manual"
)

// Valid reports whether s is one of the three states. A provider's answer is
// filtered by it, so an unrecognised state can never reach a prompt as though
// it meant something.
func (s CorrelationState) Valid() bool {
	switch s {
	case CorrelationConfirmed, CorrelationRejected, CorrelationManual:
		return true
	}
	return false
}

// CorrelationRequest asks what has been decided about correlating datasources.
//
// DatasourceIDs is the pair the agent is about to hop between, or every
// datasource on a run when the caller is assembling the routing contract and
// wants whatever exists. A provider answers about the pairs it can form from
// that list and says nothing about datasources it was not asked about.
type CorrelationRequest struct {
	ProjectID     string
	DatasourceIDs []string
}

// CorrelationKey is one decided pairing between two datasources.
//
// Symmetric: it is stated from one side, and it is true both ways. If the
// values line up reading A then filtering B, they line up reading B then
// filtering A, and a decision recorded in one orientation holds in the other.
// The sides are named DatasourceID/WithDatasourceID rather than source/target
// for that reason — source and target are hop directions, and this is not one.
type CorrelationKey struct {
	// DatasourceID owns SourceField, spelled as a query to that datasource
	// must spell it.
	DatasourceID string
	SourceField  string

	// WithDatasourceID owns AnchorColumns, qualified by their table because
	// the same column name occurs in several tables of a real warehouse and
	// "join on order_id" is not actionable without saying which one.
	WithDatasourceID string
	AnchorColumns    []string

	// Grain is how finely the two sides can be correlated on this key, in the
	// provider's own vocabulary. Rendered for the agent; never interpreted
	// here, so a provider can name a grain this package has never heard of.
	Grain string

	State CorrelationState

	// Reason is the provider's own sentence about a rejected pairing: why the
	// two sides do not hold the same values. It is quoted to the agent, so a
	// prohibition arrives with its evidence rather than asserting itself. A
	// rejection that arrives without one gets the generic sentence below.
	Reason string
}

// CorrelationProviderFunc answers a CorrelationRequest. An error means the
// provider could not reach its evidence; callers must treat that as "nothing
// is known" and carry on, never as a reason to stop — evidence being
// unreachable is not grounds for failing a run somebody asked for.
type CorrelationProviderFunc func(ctx context.Context, req CorrelationRequest) ([]CorrelationKey, error)

var (
	correlationMu           sync.RWMutex
	correlationProvider     CorrelationProviderFunc
	correlationProviderName string
)

// RegisterCorrelationProvider registers fn as THE correlation provider.
// Plugins call this from init() with a blank import. Empty name or nil fn
// panics, as does a second registration: like the join-key validator and
// unlike the filter registries, there is no meaningful way to combine two
// answers to "what was decided about this pair", and picking one silently
// would make the answer depend on link order.
func RegisterCorrelationProvider(name string, fn CorrelationProviderFunc) {
	if name == "" {
		panic("agentplugin: RegisterCorrelationProvider called with empty name")
	}
	if fn == nil {
		panic(fmt.Sprintf("agentplugin: RegisterCorrelationProvider %q called with nil fn", name))
	}
	correlationMu.Lock()
	defer correlationMu.Unlock()
	if correlationProvider != nil {
		panic(fmt.Sprintf("agentplugin: CorrelationProvider %q already registered (%q)", name, correlationProviderName))
	}
	correlationProvider = fn
	correlationProviderName = name
}

// genericRejectionReason is what a rejected pairing says when its provider
// supplied no sentence of its own.
//
// A rejection is quoted to a model as an instruction not to do something, and
// an instruction with no reason attached is the kind a model talks itself out
// of. This is deliberately about values rather than about who decided: the
// provider knows who, this package does not, and a fabricated attribution
// would be worse than a plain statement of the fact.
const genericRejectionReason = "this pairing was reviewed and rejected: the two sides do not hold the same values, " +
	"so filtering one by the other correlates unrelated records"

// Correlations asks the registered provider about req.
//
// With no provider wired it returns nothing and no error, so callers need no
// separate "is one configured" branch: a deployment where nobody has recorded
// a decision and a deployment that cannot record one are the same answer —
// nothing is known about this pair.
//
// Keys whose state is not one of the three are dropped rather than passed on.
// Everything downstream renders State as an instruction, and a state nobody
// recognises would render as neither "use this" nor "do not", which is the one
// outcome a caller cannot act on.
func Correlations(ctx context.Context, req CorrelationRequest) ([]CorrelationKey, error) {
	correlationMu.RLock()
	fn, name := correlationProvider, correlationProviderName
	correlationMu.RUnlock()

	if fn == nil {
		return nil, nil
	}
	keys, err := callCorrelationProvider(ctx, fn, req)
	if err != nil {
		return nil, fmt.Errorf("correlation provider %q: %w", name, err)
	}
	out := make([]CorrelationKey, 0, len(keys))
	for _, k := range keys {
		if !k.State.Valid() {
			continue
		}
		if k.State == CorrelationRejected && k.Reason == "" {
			k.Reason = genericRejectionReason
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// callCorrelationProvider invokes fn, converting a panic into an error.
//
// A provider is another module's code, reached through a blank import: a nil
// map or a bad index inside it would otherwise escape into whatever goroutine
// asked. A discovery run has no recover of its own, so one lookup could take
// the agent process down — and with it every other run on it — over a question
// whose honest answer is "nothing is known".
func callCorrelationProvider(ctx context.Context, fn CorrelationProviderFunc, req CorrelationRequest) (keys []CorrelationKey, err error) {
	defer func() {
		if r := recover(); r != nil {
			// keys needs no clearing: a panicking call never returned, so the
			// named result still holds its zero value. Assigning it again
			// would be a line that cannot change what this function does.
			err = fmt.Errorf("panicked: %v", r)
		}
	}()
	return fn(ctx, req)
}

// ResetCorrelationProviderForTest drops the registered provider. Test-only.
func ResetCorrelationProviderForTest() {
	correlationMu.Lock()
	defer correlationMu.Unlock()
	correlationProvider = nil
	correlationProviderName = ""
}
