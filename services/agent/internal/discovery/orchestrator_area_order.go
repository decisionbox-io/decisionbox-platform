package discovery

import (
	"sort"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// runTypeFailed is what a run whose every analysis area failed records on the
// result it still saves. See validationStaleness.
const runTypeFailed = "failed"

// judgedAreasOf returns the areas that got at least one finding judged in this
// discovery.
//
// Judged, not validated: a finding the consolidation pass folded
// (skipped_duplicate) was answered without a check, and an area whose findings
// were all folded does not need the budget moved towards it. The one state
// that says "this area's findings were never looked at" is
// skipped_budget_cap — the budget ran out before their turn — and a finding
// with no validation block at all never reached the phase.
func judgedAreasOf(disc *models.DiscoveryResult) map[string]bool {
	if disc == nil {
		return nil
	}
	out := make(map[string]bool)
	for i := range disc.Insights {
		ins := &disc.Insights[i]
		if ins.AnalysisArea == "" || ins.Validation == nil {
			continue
		}
		switch ins.Validation.Combined {
		case "", valmodels.StatusSkippedBudgetCap:
			continue
		}
		out[ins.AnalysisArea] = true
	}
	return out
}

// validationStaleness reports, per analysis area, how many discoveries back it
// last had a finding judged: 0 in the most recent usable discovery, 1 in the
// one before it, and the window's length for an area nothing in the window
// judged at all.
//
// Why the history decides this run's order: validation charges a fixed number
// of checks per run inside the analysis-area loop, so the areas that run first
// spend the budget and the rest are stamped skipped_budget_cap — and a capped
// finding is dropped from recommendation generation too, because that status is
// not in the recommender's eligible set. #560 fixed the area order by id, which
// made two runs of a project comparable and made that starvation stable with
// it: the same tail areas lost their checks every run.
//
// Why STALENESS rather than "starved last time", which is what this started
// as: with several areas starved at once, "starved first, then by id" re-sorts
// that group by id every run, so the lowest id always goes first and a high-id
// area can starve forever. A simulation of three areas and a budget of one
// walks a, b, a, b — and never c. Ranking by how long an area has gone without
// a check rotates by construction, because an area that gets its checks ranks
// freshest and goes to the back of the queue. It needs no cursor carried
// between runs and no new state.
//
// It reads the history loadPreviousDiscoveryContext has already fetched, so it
// costs no query of its own and the two look at the same window.
//
// KNOWN LIMITATION, recorded rather than fixed. The order is a pure function of
// this list, so two attempts of one run agree on it — unless another run of the
// same project COMPLETES while this one is paused, which changes the list under
// a resumed attempt. The order then differs from the one the first attempt
// used, and a fresh area can be consolidated before a restored area it would
// otherwise have been compared against: the cost is one validation check spent
// on a restatement that the end-of-run duplicate check then marks anyway.
// Holding the order steady across attempts means persisting it in the run's
// checkpoint, which is a schema decision this change does not need to make.
func validationStaleness(recent []*models.DiscoveryResult, runID string) map[string]int {
	usable := make([]*models.DiscoveryResult, 0, len(recent))
	for _, disc := range recent {
		switch {
		case disc == nil:
			continue
		// This run's own result. On a resumed run the attempt that crashed is
		// the most recent discovery, and a partial result's record describes
		// how far that attempt got rather than what the project's completed
		// runs managed. Historical documents carry no run id, so they are
		// never matched.
		case runID != "" && disc.RunID == runID:
			continue
		// A run whose every area failed saved a result with no findings in
		// it. Counting it as a discovery in which nothing was judged would
		// age every area by one for a run that never ran.
		case disc.RunType == runTypeFailed:
			continue
		}
		usable = append(usable, disc)
	}
	if len(usable) == 0 {
		return nil
	}
	// The repository hands the list back newest first, so the index IS the
	// age. First write wins: an area judged in several discoveries takes the
	// freshest.
	out := make(map[string]int)
	for age, disc := range usable {
		for area := range judgedAreasOf(disc) {
			if _, seen := out[area]; !seen {
				out[area] = age
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	out[unjudgedArea] = len(usable)
	return out
}

// unjudgedArea is the key validationStaleness stores the window length under,
// so the order can rank an area the window never judged without the caller
// having to know how long the window was. Not a legal area id: ids come from
// the pack's own keys.
const unjudgedArea = ""

// orderAreasStalestFirst puts the areas that have gone longest without a check
// at the front, so the validation budget reaches a different part of the pack
// than it did last time.
//
// Ties keep the order they arrived in, which is the id order #560 fixed — so
// this expresses exactly one preference, how long an area has waited, and no
// preference at all between areas that have waited equally. Nothing here reads
// the pack's priority field: which areas matter more is a product decision
// nobody has made.
//
// An area the window never judged ranks as stale as the window is long, which
// puts a newly added area ahead of areas already being checked. It costs
// nothing when that area reports nothing.
func orderAreasStalestFirst(areas []AnalysisArea, staleness map[string]int) []AnalysisArea {
	if len(staleness) == 0 || len(areas) < 2 {
		return areas
	}
	never := staleness[unjudgedArea]
	age := func(id string) int {
		if a, ok := staleness[id]; ok {
			return a
		}
		return never
	}
	out := make([]AnalysisArea, len(areas))
	copy(out, areas)
	sort.SliceStable(out, func(i, j int) bool {
		return age(out[i].ID) > age(out[j].ID)
	})
	return out
}
