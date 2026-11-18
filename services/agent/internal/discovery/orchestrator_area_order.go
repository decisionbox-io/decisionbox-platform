package discovery

import (
	"context"
	"sort"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	applog "github.com/decisionbox-io/decisionbox/services/agent/internal/log"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// starvedBudgetState is the validation state a finding carries when the
// per-run budget ran out before its turn. The shared constant, not a literal:
// it is the only state that says "this area's findings were never judged",
// which is what this file acts on — a finding the consolidation pass folded
// (skipped_duplicate) was not starved, it was answered.
const starvedBudgetState = valmodels.StatusSkippedBudgetCap

// previousRunStarvedAreas returns the analysis areas whose findings the
// project's previous discovery left unvalidated because the validation budget
// ran out.
//
// Why the PREVIOUS run decides this run's order: the budget is a fixed number
// of checks per run, the areas are prompted independently, and which of them
// reaches the budget first was until now the order a Go map happened to yield.
// #560 made that order stable (by id), which made two runs comparable — and
// made the starvation stable too: the same tail areas lose their checks every
// time. Reading the previous run's outcome is the cheapest signal available
// that says which areas those were, and it needs no new configuration and no
// judgement about which areas matter more.
//
// See lastCompleteDiscovery for why this run's own result is skipped.
func (o *Orchestrator) previousRunStarvedAreas(ctx context.Context) map[string]bool {
	if o.discoveryRepo == nil {
		return nil
	}
	recent, err := o.discoveryRepo.ListRecent(ctx, o.projectID, 3)
	if err != nil {
		applog.WithError(err).Warn("Could not read the previous discovery; areas run in id order")
		return nil
	}
	return starvedAreasOf(lastCompleteDiscovery(recent, o.runID))
}

// lastCompleteDiscovery picks the most recent discovery that is not this run's
// own, from a list the repository already sorted newest first.
//
// Its own result is skipped for the reason loadPreviousDiscoveryContext skips
// it: on a resumed run the attempt that crashed is the most recent discovery,
// and a partial result's starvation describes how far that attempt got rather
// than what the project's last complete run managed. Historical documents
// carry no run id, so they are never matched.
func lastCompleteDiscovery(recent []*models.DiscoveryResult, runID string) *models.DiscoveryResult {
	for _, disc := range recent {
		if disc == nil || (runID != "" && disc.RunID == runID) {
			continue
		}
		return disc
	}
	return nil
}

// starvedAreasOf is the per-discovery half, kept pure so the rule is testable
// without a repository.
func starvedAreasOf(disc *models.DiscoveryResult) map[string]bool {
	if disc == nil {
		return nil
	}
	out := make(map[string]bool)
	for i := range disc.Insights {
		ins := &disc.Insights[i]
		if ins.AnalysisArea == "" {
			continue
		}
		if ins.Validation != nil && ins.Validation.Combined == starvedBudgetState {
			out[ins.AnalysisArea] = true
		}
	}
	return out
}

// orderAreasStarvedFirst puts the areas the previous discovery starved at the
// front, each group keeping the order it arrived in.
//
// A stable partition rather than a sort key, so the id order #560 fixed still
// decides everything within a group: this expresses one preference — an area
// whose findings went unjudged last time gets its turn at the budget before an
// area that was already judged — and no preference at all between areas on the
// same side of that line. Nothing here reads the pack's priority field; which
// areas matter more is a product decision nobody has made.
func orderAreasStarvedFirst(areas []AnalysisArea, starved map[string]bool) []AnalysisArea {
	if len(starved) == 0 || len(areas) < 2 {
		return areas
	}
	out := make([]AnalysisArea, len(areas))
	copy(out, areas)
	sort.SliceStable(out, func(i, j int) bool {
		return starved[out[i].ID] && !starved[out[j].ID]
	})
	return out
}
