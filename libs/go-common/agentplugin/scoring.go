package agentplugin

import (
	"context"
	"fmt"
	"sync"

	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
)

// The seam through which deployment-wide scoring configuration reaches a run.
//
// Severity is a grounded magnitude times the analysis area's weight, and the
// weight is configuration: a pack declares a risk taxonomy with a weight per
// category, and a project may override any category's weight for itself. Both
// are read from the project document. What the project document cannot hold is
// a statement that outlives one project -- a deployment's standing priorities,
// true of every project it runs. Whoever can answer that is a plugin, its
// answer lives somewhere the agent must not read directly, and with nothing
// registered the answer is "nothing configured".
//
// The shape is deliberately the discovery-policy registry's in policy.go, down
// to the clause order: a single provider, last registrant wins, best-effort,
// never able to fail a run.

// ScoringOverride is deployment-wide scoring configuration for one project's
// run.
type ScoringOverride struct {
	// CategoryWeights weights business-risk categories, keyed by category id.
	// Applied above the pack's declared taxonomy and below the project's own
	// category weights -- see scoring.ResolveSeverity for the full order.
	CategoryWeights map[string]float64
}

// ScoringConfigProvider supplies the deployment-wide scoring configuration that
// applies to a project's runs.
type ScoringConfigProvider interface {
	// ScoringConfig returns the override for projectID. Must be safe for
	// concurrent use and honour ctx cancellation.
	ScoringConfig(ctx context.Context, projectID string) (ScoringOverride, error)
	// Name is a stable identifier for telemetry.
	Name() string
}

var (
	scoringMu       sync.RWMutex
	scoringProvider ScoringConfigProvider
)

// RegisterScoringConfigProvider installs p as the scoring-configuration
// provider. There is a single provider and the last registrant wins, as with
// the discovery policy. Registering nil, or a provider with an empty Name(),
// panics -- those are programmer errors and init() is where they surface.
func RegisterScoringConfigProvider(p ScoringConfigProvider) {
	if p == nil {
		panic("agentplugin: RegisterScoringConfigProvider called with nil provider")
	}
	if p.Name() == "" {
		panic("agentplugin: ScoringConfigProvider.Name() returned empty string")
	}
	scoringMu.Lock()
	scoringProvider = p
	scoringMu.Unlock()
}

// ResolveScoringConfig returns the deployment-wide scoring configuration for
// projectID.
//
// Best-effort, and it never fails a run: with no provider registered it returns
// the zero override and a nil error, which is the default every deployment is
// in until a provider is registered. If the provider errors or panics it
// returns the zero override and an error naming the provider, so the caller can log it and score
// the run on the pack and project configuration it already has. A severity
// weight is a preference; losing it costs a ranking, and failing the run over
// it would cost the whole result.
//
// The answer is NORMALISED before it is returned, so no caller re-validates.
// An entry is dropped -- not clamped, not corrected -- when its weight is
// outside (0, scoring.MaxSeverityWeight] or its id is not shaped like a risk
// category id. The id check is not cosmetic: the resolved weights are pinned
// on the run document, and a key that document cannot store would fail the pin
// and abort the run, which is the one outcome this seam must never cause.
func ResolveScoringConfig(ctx context.Context, projectID string) (override ScoringOverride, err error) {
	scoringMu.RLock()
	p := scoringProvider
	scoringMu.RUnlock()
	if p == nil {
		return ScoringOverride{}, nil
	}
	defer func() {
		if r := recover(); r != nil {
			override = ScoringOverride{}
			err = fmt.Errorf("agentplugin: scoring config provider %q panicked: %v", safeScoringName(p), r)
		}
	}()
	got, perr := p.ScoringConfig(ctx, projectID)
	if perr != nil {
		// Wrapped with the provider's name, so the one log line a caller
		// writes says which plugin failed without asking the registry again.
		return ScoringOverride{}, fmt.Errorf("agentplugin: scoring config provider %q: %w", safeScoringName(p), perr)
	}
	return normaliseScoringOverride(got), nil
}

// normaliseScoringOverride drops every entry the engine would refuse. A map
// left empty is returned nil, because "nothing legal was configured" and
// "nothing was configured" are the same statement to every reader downstream.
func normaliseScoringOverride(in ScoringOverride) ScoringOverride {
	var out ScoringOverride
	for id, w := range in.CategoryWeights {
		if !scoring.ValidRiskCategoryID(id) || !scoring.ValidSeverityWeight(w) {
			continue
		}
		if out.CategoryWeights == nil {
			out.CategoryWeights = make(map[string]float64, len(in.CategoryWeights))
		}
		out.CategoryWeights[id] = w
	}
	return out
}

func safeScoringName(p ScoringConfigProvider) (name string) {
	defer func() {
		if recover() != nil {
			name = "<unknown:Name() panicked>"
		}
	}()
	return p.Name()
}

// resetScoringForTest clears the registered provider. Test-only.
func resetScoringForTest() {
	scoringMu.Lock()
	scoringProvider = nil
	scoringMu.Unlock()
}
