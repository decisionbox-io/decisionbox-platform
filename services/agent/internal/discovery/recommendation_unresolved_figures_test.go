package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// TestUnresolvedFigureRefsDropTheRecommendation is the gap run 16 opened
// between telemetry and behaviour.
//
// That run persisted three recommendations whose every number was a dangling
// reference -- twelve of them, counted correctly as figures_unresolved=12,
// logged with a warning naming each one -- and shipped all three anyway. The
// verifier then read them, and blessed two of the three as "supported". A
// reader would have seen literal {{f1}} where a currency amount belongs.
//
// Leaving an unresolved reference visible in the prose is deliberate and
// right: a hole in the sentence tells a reader nothing, and 0.00% reads like a
// finding. But "visible" was doing all the work, and the only thing watching
// was a log line. A recommendation whose numbers never resolved is not
// publishable, and shipping nothing beats shipping a placeholder.
func TestUnresolvedFigureRefsDropTheRecommendation(t *testing.T) {
	ins := bandInsight()

	// The run-16 shape: references written, no figures array declared, so
	// there is nothing for them to resolve against.
	undeclared := models.Recommendation{
		Title:             "Correct the {{f1}} anomaly across {{f2}} items",
		Description:       "The {{f3}} balance distorts the ledger.",
		RelatedInsightIDs: []string{ins.ID},
	}
	// A reference whose id the resolving pattern cannot even match. It reaches
	// the prose by a different route -- the schema accepts any string as an id
	// -- and reFigureRef does not match a hyphen, which is the whole reason
	// reAnyRef exists.
	malformed := models.Recommendation{
		Title:             "Correct the {{revenue-total}} anomaly",
		Description:       "A plain description.",
		RelatedInsightIDs: []string{ins.ID},
	}
	// Declared, resolvable, renders to a real number.
	sound := models.Recommendation{
		Title:       "Win back {{f1}} buyers",
		Description: "The program targets {{f1}} buyers across both bands.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureSum,
			Refs: []models.FigureRef{ref(ins.ID, "f1"), ref(ins.ID, "f2")},
		}},
		RelatedInsightIDs: []string{ins.ID},
	}

	recs := []models.Recommendation{undeclared, malformed, sound}
	usWriter.attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	usWriter.renderRecommendationFigures(recs)

	kept, dropped := dropRecommendationsWithUnresolvedFigures(recs)

	if dropped != 2 {
		t.Errorf("dropped = %d, want 2 (the undeclared and the malformed reference)", dropped)
	}
	if len(kept) != 1 {
		t.Fatalf("kept %d recommendations, want 1", len(kept))
	}
	if !strings.HasPrefix(kept[0].Title, "Win back") {
		t.Errorf("kept the wrong recommendation: %q", kept[0].Title)
	}
	for _, r := range kept {
		for _, field := range []string{r.Title, r.Description} {
			if strings.Contains(field, "{{") {
				t.Errorf("a kept recommendation still carries a placeholder: %q", field)
			}
		}
	}
}

// TestUnresolvedFigureDropIsCountedSeparately keeps the new reason legible
// next to the three that already exist.
//
// The per-reason counters are how an operator tells a hallucinated citation
// from a parse failure from this. Folding a figure drop into one of the
// others, or into the total alone, would make a run that produced nothing
// look like a run that produced nothing for a reason we already knew about.
func TestUnresolvedFigureDropIsCountedSeparately(t *testing.T) {
	step := &models.RecommendationStep{
		RecommendationsDroppedParse:      1,
		RecommendationsDroppedMissingIDs: 2,
		RecommendationsDroppedUnknownID:  3,
	}
	applyUnresolvedFigureDropStats(step, nil, 4)

	if step.RecommendationsDroppedUnresolvedFigures != 4 {
		t.Errorf("RecommendationsDroppedUnresolvedFigures = %d, want 4",
			step.RecommendationsDroppedUnresolvedFigures)
	}
	if want := 1 + 2 + 3 + 4; step.RecommendationsDropped != want {
		t.Errorf("RecommendationsDropped = %d, want %d -- the four reasons are distinct drops "+
			"in the same run, so the total is their sum", step.RecommendationsDropped, want)
	}
}

// TestUnresolvedFigureDropSyncsTheStep mirrors the reason
// applyRecommendationDropStats syncs its own: the persisted step must carry
// the kept slice, or the log keeps rows the counters say were discarded.
func TestUnresolvedFigureDropSyncsTheStep(t *testing.T) {
	kept := []models.Recommendation{{Title: "Kept"}}
	step := &models.RecommendationStep{
		Recommendations: []models.Recommendation{{Title: "Kept"}, {Title: "Dropped"}},
	}
	applyUnresolvedFigureDropStats(step, kept, 1)

	if len(step.Recommendations) != 1 || step.Recommendations[0].Title != "Kept" {
		t.Errorf("step.Recommendations = %+v, want only the kept recommendation", step.Recommendations)
	}
}

// TestUnresolvedFigureDropExplainsAnEmptyResult guards the failure this gate
// introduces.
//
// Dropping every recommendation is the right call when every one of them
// references a number that resolved to nothing -- and it leaves the phase
// reporting zero recommendations, which is exactly what a model with nothing
// worth recommending reports. That ambiguity is the silent empty section
// several earlier issues went to work on; a gate added without a reason code
// would hand it straight back.
func TestUnresolvedFigureDropExplainsAnEmptyResult(t *testing.T) {
	step := &models.RecommendationStep{}
	applyUnresolvedFigureDropStats(step, nil, 3)
	if step.Status != statusRecommendationFiguresUnresolved {
		t.Errorf("Status = %q, want %q so the dashboard can say why the section is empty",
			step.Status, statusRecommendationFiguresUnresolved)
	}
}

// TestUnresolvedFigureDropKeepsTheMoreSpecificStatus pins the precedence.
//
// A parse error happens earlier in the phase and names a different cause. If
// the gate then overwrote it, a run whose response could not be decoded would
// be reported as one whose figures did not resolve, and the retry telemetry
// beside it would make no sense.
func TestUnresolvedFigureDropKeepsTheMoreSpecificStatus(t *testing.T) {
	step := &models.RecommendationStep{Status: statusRecommendationParseError}
	applyUnresolvedFigureDropStats(step, nil, 3)
	if step.Status != statusRecommendationParseError {
		t.Errorf("Status = %q, want the earlier, more specific %q",
			step.Status, statusRecommendationParseError)
	}
}

// TestUnresolvedFigureDropLeavesASurvivingResultUnlabelled keeps the status to
// the case it describes: some recommendations resolving and some not is a
// partial result, not an empty one, and the per-reason counter already carries
// the detail.
func TestUnresolvedFigureDropLeavesASurvivingResultUnlabelled(t *testing.T) {
	step := &models.RecommendationStep{}
	applyUnresolvedFigureDropStats(step, []models.Recommendation{{Title: "Kept"}}, 2)
	if step.Status != "" {
		t.Errorf("Status = %q, want empty: recommendations survived, so the result is partial", step.Status)
	}
}
