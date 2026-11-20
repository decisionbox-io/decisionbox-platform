package agentplugin

import (
	"context"
	"fmt"
	"math"
	"sync"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
)

// The seam through which a deployment's own severity configuration reaches a
// run.
//
// Severity is a grounded magnitude times the analysis area's weight, and the
// weight is configuration: a pack declares a risk taxonomy with a weight per
// category, an area maps to categories, and a project may override a
// category's weight. All of that is already read from the project document.
// What a deployment cannot say through it is anything that outlives one
// project -- a vertical's standing priorities -- or anything the project
// document has no field for, which is what an absolute money threshold is.
//
// So this is a registry, not a field: whoever can answer those questions is a
// plugin, the answer lives in a collection the platform must not read
// directly, and with nothing registered the answer is "nothing configured".
// The discovery-policy registry one file over exists for the same reason and
// is deliberately the same shape, down to the clause order -- a second shape
// for a single-provider best-effort lookup is a second set of edge cases to
// get right.

// ScoringOverride is a deployment's own severity configuration for one
// (project, datasource). An alias of the model type: the same value is stored
// in a collection, returned over HTTP, handed back by the provider below and
// folded into a calibration, and a conversion at any of those seams is where
// a field added to one copy stops reaching the others.
type ScoringOverride = gomodels.ScoringOverride

// ScoringConfigProvider resolves the override for one project and datasource.
// The enterprise insight-scoring plugin implements it over its Mongo
// collection (deployment default, then the project's own document).
type ScoringConfigProvider interface {
	// ScoringConfig returns the override for projectID and warehouseID. Must
	// be safe for concurrent use and honour ctx cancellation.
	//
	// warehouseID is the datasource the run is scored under -- the one whose
	// pack supplied its analysis areas. The money thresholds are per
	// datasource because the currency is; the category weights are not, and a
	// provider returns the same ones whatever is asked for.
	ScoringConfig(ctx context.Context, projectID, warehouseID string) (ScoringOverride, error)
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
// panics -- those are programmer errors and init() is where they should
// surface.
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

// ResolveScoringConfig returns the override for projectID and warehouseID.
//
// Best-effort, and it never fails a run: with no provider registered it
// returns the ZERO override and a nil error, which is the community case and
// the case every deployment is in until someone saves a weight. If the
// provider errors or panics it returns the zero override and the error, so the
// caller can log it and score the run on the pack and project configuration
// it already has. A severity weight is a preference; losing it costs a
// ranking, and failing the run over it would cost the whole result.
//
// The answer is NORMALISED before it is returned, so no caller re-validates:
// an id with no name, a weight outside (0, MaxSeverityWeight], a band with a
// non-positive threshold or an unknown band word are DROPPED rather than
// clamped or corrected. Clamping would act on a number nobody wrote, and a
// value that reached here illegal got past validation by being hand-edited.
// This mirrors how the discovery policy normalises an unknown mode rather than
// passing it on.
func ResolveScoringConfig(ctx context.Context, projectID, warehouseID string) (override ScoringOverride, err error) {
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
	got, perr := p.ScoringConfig(ctx, projectID, warehouseID)
	if perr != nil {
		return ScoringOverride{}, perr
	}
	return normaliseScoringOverride(got), nil
}

// normaliseScoringOverride drops everything the engine would refuse, so what
// comes back can be folded into a calibration without a second opinion.
//
// A map that ends up empty is returned NIL rather than empty, and a slice the
// same, because "nothing legal was configured" and "nothing was configured"
// are the same statement to every reader downstream -- and keeping them
// distinguishable would mean a deployment whose only weight was illegal
// looking configured while behaving unconfigured.
func normaliseScoringOverride(in ScoringOverride) ScoringOverride {
	// NORMALISED here, where a provider's answer is accepted, so the value
	// pinned on the run is the one the engine compares. A ladder currency is
	// read by the engine, by the trend basis and by the stored sentence, and
	// each of those normalising for itself is how `usd` came to be stored as a
	// currency distinct from `USD`.
	out := ScoringOverride{AmountCurrency: scoring.CurrencySymbol(in.AmountCurrency)}

	weights := make(map[string]float64, len(in.CategoryWeights))
	for id, w := range in.CategoryWeights {
		if id == "" || !scoring.ValidSeverityWeight(w) {
			continue
		}
		weights[id] = w
	}
	if len(weights) > 0 {
		out.CategoryWeights = weights
	}

	bands := make([]gomodels.MagnitudeBand, 0, len(in.AmountBands))
	for _, b := range in.AmountBands {
		// !(b.At > 0) rather than b.At <= 0, because NaN compares false
		// against everything.
		//
		// A hand-edited BSON threshold of NaN or ±Inf passed a `<= 0` check
		// and was pinned on the run document -- which BSON round-trips
		// happily and Go's JSON encoder refuses, so GET /api/v1/runs/{runId}
		// answered 200 with an empty body and the run became unreadable
		// through the API. The seam drops what it cannot price, exactly as it
		// drops an illegal weight, and a run then scores on the pack and
		// project configuration.
		if !(b.At > 0) || math.IsInf(b.At, 0) || !validMagnitudeBand(b.Band) {
			continue
		}
		bands = append(bands, b)
	}
	// And the surviving rungs have to DESCEND.
	//
	// The engine takes the first rung an amount reaches, so a ladder reading
	// `100,000 -> medium` before `1,000,000 -> high` bands a $2M amount
	// medium and makes every rung after the first unreachable. The settings
	// API refuses such a ladder, but this seam also carries what a
	// hand-edited document or another provider implementation supplies.
	//
	// Dropped whole rather than sorted: reordering would silently apply a
	// ladder nobody wrote. The weights beside it are untouched, because a
	// ladder and a weight are separate statements about the same project.
	if len(bands) > 0 && descending(bands) {
		out.AmountBands = bands
	}

	// A currency with no bands left says nothing and would be stored on a run
	// as a threshold set nobody can reach. Dropped with them.
	if out.AmountBands == nil {
		out.AmountCurrency = ""
	}
	return out
}

// validMagnitudeBand reports whether a band word is one the engine prices.
//
// The three severity bands and nothing else: a cutoff naming a fourth would
// land a figure in a band with no magnitude behind it, which scores every such
// document at zero.
// descending reports whether every rung sits strictly below the one before
// it, which is the order the engine reads them in.
func descending(bands []gomodels.MagnitudeBand) bool {
	for i := 1; i < len(bands); i++ {
		if bands[i].At >= bands[i-1].At {
			return false
		}
	}
	return true
}

func validMagnitudeBand(band string) bool {
	switch band {
	case gomodels.SeverityBandHigh, gomodels.SeverityBandMedium, gomodels.SeverityBandLow:
		return true
	}
	return false
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
