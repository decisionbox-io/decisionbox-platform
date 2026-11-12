package agentserver

import (
	"testing"

	gomodels "github.com/decisionbox-io/decisionbox/libs/go-common/models"
	"github.com/decisionbox-io/decisionbox/libs/go-common/notify"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The run-completed notification is the one surface a reader sees without
// opening the dashboard, and its three counters are the whole summary of a
// run. They used to count the severity WORD, and `critical` was the top
// severity word.
//
// Severity is derived now and its vocabulary is three bands, so no insight
// writes `critical` as a severity any more: a counter still reading the word
// would have reported "0 critical" on every run from the moment the
// derivation shipped, silently and with nothing failing. That is the whole
// reason the severity vocabulary and the priority matrix had to land in one
// change rather than two.

func scored(tier string) models.Insight {
	return models.Insight{
		ID: "i-" + tier, Name: "n-" + tier,
		Scoring: &models.Scoring{PriorityTier: tier},
	}
}

func legacy(severity string) models.Insight {
	return models.Insight{ID: "legacy-" + severity, Name: "legacy-" + severity, Severity: severity}
}

func TestTheCompletionEventCountsTiersNotSeverityWords(t *testing.T) {
	insights := []models.Insight{
		scored(gomodels.PriorityTierCritical),
		scored(gomodels.PriorityTierCritical),
		scored(gomodels.PriorityTierInvestigate),
		scored(gomodels.PriorityTierHigh),
		scored(gomodels.PriorityTierLow),
	}

	if got := countByTier(insights, gomodels.PriorityTierCritical); got != 2 {
		t.Errorf("critical = %d, want 2", got)
	}
	if got := countByTier(insights, gomodels.PriorityTierInvestigate); got != 1 {
		t.Errorf("investigate = %d, want 1", got)
	}
	if got := countByTier(insights, gomodels.PriorityTierHigh); got != 1 {
		t.Errorf("high = %d, want 1", got)
	}
	// And the regression itself: a run of derived insights must not report
	// zero of the top tier.
	if countByTier(insights, gomodels.PriorityTierCritical) == 0 {
		t.Error(`the completion event reported "0 critical" on a run that had two`)
	}
}

// TestAMixedRunCountsBothKindsOfDocument is the case a pure-new fixture would
// have missed: a run whose findings include insights from before the
// derivation, which have a severity word and no tier.
func TestAMixedRunCountsBothKindsOfDocument(t *testing.T) {
	insights := []models.Insight{
		scored(gomodels.PriorityTierCritical),
		legacy("critical"),
		scored(gomodels.PriorityTierHigh),
		legacy("high"),
		legacy("low"),
		scored(gomodels.PriorityTierInvestigate),
	}

	if got := countByTier(insights, gomodels.PriorityTierCritical); got != 2 {
		t.Errorf("critical = %d, want 2 (one derived tier, one legacy word)", got)
	}
	if got := countByTier(insights, gomodels.PriorityTierHigh); got != 2 {
		t.Errorf("high = %d, want 2", got)
	}
	// `investigate` has no legacy equivalent -- it is the cell the old
	// vocabulary had no way to express -- so only the derived one counts.
	if got := countByTier(insights, gomodels.PriorityTierInvestigate); got != 1 {
		t.Errorf("investigate = %d, want 1; a legacy document was counted into a tier it was never triaged by", got)
	}
}

// TestADocumentWithATierIsNeverCountedByItsWord -- the fallback must be a
// fallback. A derived insight carries both a tier and a mirrored severity
// band, and counting it twice would inflate every counter on every run.
func TestADocumentWithATierIsNeverCountedByItsWord(t *testing.T) {
	both := models.Insight{
		ID: "i1", Name: "n",
		Severity: gomodels.SeverityBandHigh,
		Scoring:  &models.Scoring{PriorityTier: gomodels.PriorityTierLow},
	}
	if got := countByTier([]models.Insight{both}, gomodels.PriorityTierCritical); got != 0 {
		t.Errorf("critical = %d; the severity word was read on a document that has a tier", got)
	}
	if got := countByTier([]models.Insight{both}, gomodels.PriorityTierLow); got != 1 {
		t.Errorf("low = %d, want 1", got)
	}
}

// TestTopInsightBriefsOrderByTier -- the briefs are what a channel renders,
// and they have to interleave the two kinds of document rather than sinking
// every legacy one to the bottom of a mixed run.
func TestTopInsightBriefsOrderByTier(t *testing.T) {
	briefs := topInsightBriefs([]models.Insight{
		scored(gomodels.PriorityTierLow),
		legacy("medium"),
		scored(gomodels.PriorityTierCritical),
		scored(gomodels.PriorityTierHigh),
		legacy("critical"),
		scored(gomodels.PriorityTierInvestigate),
	}, 6)

	if len(briefs) != 6 {
		t.Fatalf("briefs = %d, want 6", len(briefs))
	}
	// Within one tier the order is the input's, because the sort is stable --
	// so `legacy-medium` precedes `i-high` here for no reason other than that
	// it arrived first, which is what TestTopInsightBriefsIsStableWithinATier
	// pins. Across tiers the order is the engine's.
	want := []string{
		"i-critical",      // critical tier
		"legacy-critical", // legacy word, same cell
		"i-investigate",
		"legacy-medium", // a trusted medium is the `high` cell
		"i-high",
		"i-low",
	}
	for i, id := range want {
		if briefs[i].ID != id {
			t.Errorf("brief %d = %q, want %q (order: %v)", i, briefs[i].ID, id, briefIDs(briefs))
		}
	}
	// The tier travels to the channel, so a template can lead with it.
	if briefs[0].PriorityTier != gomodels.PriorityTierCritical {
		t.Errorf("brief tier = %q, want critical", briefs[0].PriorityTier)
	}
	// And a legacy brief carries none rather than a guessed one -- the
	// ordering fallback is for ordering, not for labelling.
	if briefs[1].PriorityTier != "" {
		t.Errorf("a legacy brief claimed tier %q; the ordering fallback is for ordering, not labelling", briefs[1].PriorityTier)
	}
}

// TestTopInsightBriefsIsStableWithinATier -- the sort has to be stable, or two
// consecutive notifications about the same run could list the same tier's
// findings in different orders.
func TestTopInsightBriefsIsStableWithinATier(t *testing.T) {
	in := []models.Insight{
		{ID: "a", Scoring: &models.Scoring{PriorityTier: gomodels.PriorityTierHigh}},
		{ID: "b", Scoring: &models.Scoring{PriorityTier: gomodels.PriorityTierHigh}},
		{ID: "c", Scoring: &models.Scoring{PriorityTier: gomodels.PriorityTierHigh}},
	}
	for i := 0; i < 5; i++ {
		got := briefIDs(topInsightBriefs(in, 3))
		if got[0] != "a" || got[1] != "b" || got[2] != "c" {
			t.Fatalf("run %d reordered a tier: %v", i, got)
		}
	}
}

func briefIDs(briefs []notify.InsightBrief) []string {
	out := make([]string, len(briefs))
	for i, b := range briefs {
		out[i] = b.ID
	}
	return out
}

// TestALegacyInsightWithNoSeverityIsCountedNowhere -- an insight that
// produced no severity word at all, from before the field was derived.
//
// `investigate` has no legacy equivalent, so its lookup yields "" — and so
// does an empty severity. An equality check alone would count every such
// document into a tier nothing ever triaged it by, which is the one direction
// a count must not be wrong in: it would invent urgency.
func TestALegacyInsightWithNoSeverityIsCountedNowhere(t *testing.T) {
	blank := []models.Insight{{ID: "i1", Name: "n"}, {ID: "i2", Name: "n2"}}
	for _, tier := range []string{
		gomodels.PriorityTierCritical, gomodels.PriorityTierInvestigate,
		gomodels.PriorityTierHigh, gomodels.PriorityTierLow,
	} {
		if got := countByTier(blank, tier); got != 0 {
			t.Errorf("tier %q counted %d insights that carry no severity at all", tier, got)
		}
	}
}
