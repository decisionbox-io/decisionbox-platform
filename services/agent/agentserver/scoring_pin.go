package agentserver

import (
	"context"
	"fmt"
	"time"

	"github.com/decisionbox-io/decisionbox/libs/go-common/agentplugin"
	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/scoring"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// Pinning a run's severity weighting, so a resume cannot be scored under two
// sets of weights.
//
// Severity is magnitude x the analysis area's weight, and the weight is
// configuration: a project's category weights, the pack's taxonomy, and each
// area's own override. A resume can happen days after the attempt it
// continues, and a weight changed in between would score the second half of a
// run against different numbers from the first -- leaving a result with no
// meaningful ranking across it. The run's step budget, area list and effort
// are already pinned on the document for exactly this reason; this is the
// same decision one field over.

// scoringConfigStore is the two run-repository calls this seam makes.
//
// An interface rather than *database.RunRepository because the one thing worth
// asserting here is WHICH of the two a resume makes: a resume that pinned
// would write on a run that recorded nothing — and the write is conditional on
// absence, so it would succeed, and score the rest of a legacy run against
// today's weights while its restored areas keep their unweighted ones. That is
// a branch, not a value, and a fake is the only way to see it.
type scoringConfigStore interface {
	GetByID(ctx context.Context, runID string) (*models.DiscoveryRun, error)
	PinScoringConfig(ctx context.Context, runID string, want gomodels.RunScoringConfig) (*gomodels.RunScoringConfig, error)
}

// pinRunScoringConfig returns the severity weighting this run is scored under,
// writing it on the run document if this is the attempt that starts it.
//
// Resolved from the project here, at the one place that knows both the project
// and the run, and then never again: the repository's write is conditional on
// the field being absent, so attempt 1 stores this answer and every later
// attempt gets attempt 1's back.
//
// The INPUTS are stored rather than the resolved per-area weights. A later
// attempt therefore re-derives the weight and the risk flags through the same
// engine code as the first, and cannot disagree with it -- where storing the
// output would put a second copy of the "largest of its categories" rule in
// the database and leave the flags unpinned.
//
// A RESUME never writes. It reads whatever is pinned and takes nil for an
// answer, and that is the case the whole field exists for: a run started
// before this existed was scored with no weights at all, and its completed
// areas are RESTORED rather than re-scored. Pinning the project's current
// weights on the way back in would score the rest of that run against them
// and leave one result half unweighted and half weighted -- which is exactly
// the outcome the pin is here to prevent, arrived at by the pin itself. An
// attempt-1 process that died before this call is indistinguishable from such
// a run, and the same answer is right for it.
func pinRunScoringConfig(
	ctx context.Context,
	runRepo scoringConfigStore,
	runID string,
	project *models.Project,
	resume bool,
) (*gomodels.RunScoringConfig, error) {
	if resume {
		run, err := runRepo.GetByID(ctx, runID)
		if err != nil {
			return nil, err
		}
		if run == nil {
			return nil, fmt.Errorf("run %s not found", runID)
		}
		return run.ScoringConfig, nil
	}
	return runRepo.PinScoringConfig(ctx, runID, resolveRunScoringConfig(ctx, project))
}

// scoringLookupTimeout bounds the one provider call this function makes.
//
// Generous enough for a document read on a database that is answering, short
// enough that a database that is not does not hold up a run: the fallback is
// the configuration the run would have been scored on before this feature
// existed, so taking it costs a preference and taking too long costs the run.
//
// Configurable, because "long enough" is a property of someone's database and
// not of this code.
// Read from the SEAM's own package, not from a copy here.
//
// A provider bounds whatever it opens to answer by the same budget, so both
// sides read this variable -- and each having its own default is how they came
// to disagree: this side waited 15 seconds for a provider that gave up at 10,
// and a deployment that raised the budget got the lower number anyway.
func scoringLookupTimeout() time.Duration { return agentplugin.LookupTimeout() }

// resolveScoringConfigBounded stops WAITING after the timeout, which a context
// alone cannot do.
//
// A context deadline only signals: a provider that does not observe
// cancellation -- a driver call that has already committed to a socket read,
// or an implementation that simply ignores it -- goes on running, and the
// synchronous caller goes on waiting with it. Since this runs BEFORE the
// discovery deadline exists, that meant a stalled provider could stop a run
// from starting at all, and the fallback this whole path is built around could
// never be taken.
//
// So the lookup runs on its own goroutine and the caller stops waiting. The
// goroutine is left to finish and its result dropped -- it holds nothing the
// run needs, and abandoning a read is the point. One per run.
func resolveScoringConfigBounded(ctx context.Context, projectID, warehouseID string) (agentplugin.ScoringOverride, error) {
	type answer struct {
		override agentplugin.ScoringOverride
		err      error
	}
	// Buffered, so the goroutine never blocks writing to a channel nobody is
	// reading any more.
	done := make(chan answer, 1)

	lookupCtx, cancelLookup := context.WithTimeout(ctx, scoringLookupTimeout())
	// Cancelled by the CALLER, never by the worker.
	//
	// The worker used to cancel on its way out, which made both of the cases
	// below ready at once: it sends to a buffered channel, then cancels, and
	// a caller that had not yet reached the select saw a closed Done as well
	// as a waiting answer. Go picks at random between ready cases, so about
	// half of all SUCCESSFUL lookups reported a timeout and pinned the pack
	// and project defaults for the whole run — a configuration silently lost
	// on a coin flip, which is worse than the stall the timeout was added
	// for. Cancelling here means Done fires only when the deadline really
	// passes, and the abandoned worker is still cancelled on the way out.
	defer cancelLookup()

	go func() {
		override, err := agentplugin.ResolveScoringConfig(lookupCtx, projectID, warehouseID)
		done <- answer{override, err}
	}()

	select {
	case got := <-done:
		return got.override, got.err
	case <-lookupCtx.Done():
		// An answer that arrived anyway WINS, whatever made Done ready.
		//
		// Select picks at random between ready cases, so "the deadline
		// passed" and "the answer is waiting" being simultaneously true was
		// enough to discard a configuration the provider had successfully
		// returned. Moving the cancel to the caller above removes the way
		// this code caused that; checking here removes the question, for the
		// parent being cancelled at the same moment too. A delivered answer
		// is never worse than the fallback.
		select {
		case got := <-done:
			return got.override, got.err
		default:
		}
		return agentplugin.ScoringOverride{}, fmt.Errorf(
			"scoring configuration lookup did not answer within %s (%s); scoring on the pack and project configuration: %w",
			scoringLookupTimeout(), agentplugin.LookupTimeoutEnv, lookupCtx.Err())
	}
}

// resolveRunScoringConfig is the project's severity configuration, resolved:
// the pack's taxonomy and the project's category weights folded into one
// weight map, plus each area's mapping and override.
//
// Pure, and separate from the write, so the resolution is testable without a
// database and so the rule it follows is one function rather than a sequence
// of assignments at a call site.
//
// A project with no taxonomy, no category weights and no mapped areas resolves
// to an empty configuration. That is still PINNED, deliberately: it records
// that this run was started with nothing configured, which is what a resume
// after someone adds a taxonomy has to honour.
func resolveRunScoringConfig(ctx context.Context, project *models.Project) gomodels.RunScoringConfig {
	out := gomodels.RunScoringConfig{}
	if project == nil {
		return out
	}

	var taxonomy []gomodels.RiskCategory
	if project.Prompts != nil {
		taxonomy = project.Prompts.RiskTaxonomy

		areas := make(map[string]gomodels.RunAreaRisk, len(project.Prompts.AnalysisAreas))
		for id, cfg := range project.Prompts.AnalysisAreas {
			if len(cfg.RiskCategories) == 0 && cfg.SeverityWeight == nil {
				// Nothing configured for this area. Omitted rather than
				// stored empty, so the pinned document says what was
				// configured rather than listing every area a project has.
				continue
			}
			areas[id] = gomodels.RunAreaRisk{
				RiskCategories: cfg.RiskCategories,
				SeverityWeight: cfg.SeverityWeight,
			}
		}
		if len(areas) > 0 {
			out.Areas = areas
		}
	}

	// Through the engine's own resolution, not a fold written here: the same
	// function the precedence rules are tested against, so a project weight
	// cannot win here and lose there.
	resolved := scoring.ResolveSeverity(scoring.SeverityConfig{}, taxonomy, project.CategoryWeights)

	// Then the deployment's own configuration over the top, through the same
	// ApplyOverride the settings preview folds with.
	//
	// Resolved for ONE datasource: the primary, which is the datasource whose
	// pack supplied this run's analysis areas and whose domain the run carries
	// (warehouseDomainOr). A run is scored against one pack's area set, so one
	// resolved configuration is what the engine consumes -- and asking per
	// datasource would store thresholds against areas this run cannot produce.
	//
	// BEST-EFFORT, and the error is swallowed into a log line by the caller:
	// a severity weight is a preference, a discovery run is hours of warehouse
	// queries and model calls, and losing the first must never cost the
	// second. ResolveScoringConfig already returns the zero override on every
	// failure, so what is pinned in that case is exactly the pack and project
	// resolution above.
	// Its OWN deadline, short, because this runs before the discovery deadline
	// exists.
	//
	// The caller hands us a background context: the run's own deadline is
	// created after the configuration is pinned. So a provider that stalls on
	// a database read -- one that honours cancellation as well as one that
	// does not -- had nothing to cancel it, and the fallback this whole
	// function is built around could never be taken. The run would not start
	// at all, which is the opposite of the trade this code exists to make: a
	// severity weight is a preference, a discovery run is hours of warehouse
	// queries and model calls, and losing the first must never cost the
	// second.
	//
	// The ORIGINAL context carries on below, so persisting the fallback is not
	// cancelled by this deadline passing.
	override, err := resolveScoringConfigBounded(ctx, project.ID, warehouseIDOrDefault(project.PrimaryWarehouse()))
	if err != nil {
		applog.WithError(err).Warn("Scoring configuration provider failed; scoring this run on the pack and project configuration")
	}
	resolved = scoring.ApplyOverride(resolved, override)

	out.CategoryWeights = resolved.CategoryWeights
	// The thresholds are stored as the INPUT they are. Unlike the weights
	// there is no per-area rule over them for a later attempt to re-derive,
	// so what a resume needs is the ladder itself.
	out.AmountBands = resolved.AmountBands
	out.AmountCurrency = resolved.AmountCurrency

	return out
}
