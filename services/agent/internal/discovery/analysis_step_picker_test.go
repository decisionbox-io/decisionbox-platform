package discovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

func makeStep(n int, query, purpose string) models.ExplorationStep {
	return models.ExplorationStep{
		Step:         n,
		Query:        query,
		QueryPurpose: purpose,
	}
}

func searchFn(hits []RunStepIndexHit, err error) func(ctx context.Context, q string, opts RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
	return func(ctx context.Context, q string, opts RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
		return hits, err
	}
}

func TestPicker_VectorOnly_TopK(t *testing.T) {
	area := AnalysisArea{ID: "churn", Name: "Churn"}
	steps := []models.ExplorationStep{
		makeStep(1, "SELECT 1", "p1"),
		makeStep(2, "SELECT 2", "p2"),
		makeStep(3, "SELECT 3", "p3"),
	}
	hits := []RunStepIndexHit{
		{Step: 1, Score: 0.9},
		{Step: 2, Score: 0.7},
		{Step: 3, Score: 0.5},
	}
	picker := &AnalysisStepPicker{Search: searchFn(hits, nil)}
	res, err := picker.Pick(context.Background(), area, steps)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(res.Picked) != 3 {
		t.Fatalf("picked: got %d want 3", len(res.Picked))
	}
	if res.Picked[0].Step.Step != 1 || res.Picked[0].Source != PickSourceVector {
		t.Errorf("picked[0]: got %+v", res.Picked[0])
	}
	if res.Picked[2].Step.Step != 3 {
		t.Errorf("picked[2].Step: got %d want 3", res.Picked[2].Step.Step)
	}
}

func TestPicker_ExactMatchBoost_PromotesUnranked(t *testing.T) {
	area := AnalysisArea{
		ID:       "churn",
		Name:     "Churn",
		Keywords: []string{"retention"},
	}
	steps := []models.ExplorationStep{
		makeStep(1, "SELECT *", "look at retention by cohort"),
		makeStep(2, "SELECT *", "engagement"),
	}
	// Vector returns nothing.
	picker := &AnalysisStepPicker{Search: searchFn(nil, nil)}
	res, err := picker.Pick(context.Background(), area, steps)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(res.Picked) != 1 {
		t.Fatalf("picked: got %d want 1", len(res.Picked))
	}
	if res.Picked[0].Step.Step != 1 {
		t.Errorf("expected step 1 (matched 'retention'), got %d", res.Picked[0].Step.Step)
	}
	if res.Picked[0].Source != PickSourceExactMatch {
		t.Errorf("source: got %q want %q", res.Picked[0].Source, PickSourceExactMatch)
	}
	if res.Picked[0].Score != ExactMatchFloor {
		t.Errorf("score: got %v want %v", res.Picked[0].Score, ExactMatchFloor)
	}
}

func TestPicker_ExactMatchBoost_DoesNotDemoteHigher(t *testing.T) {
	area := AnalysisArea{
		ID:       "churn",
		Name:     "Churn",
		Keywords: []string{"churn"},
	}
	steps := []models.ExplorationStep{
		makeStep(1, "SELECT *", "churn rate by week"),
	}
	hits := []RunStepIndexHit{
		{Step: 1, Score: 0.95},
	}
	picker := &AnalysisStepPicker{Search: searchFn(hits, nil)}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 1 {
		t.Fatalf("picked: got %d want 1", len(res.Picked))
	}
	if res.Picked[0].Score != 0.95 {
		t.Errorf("score should be 0.95 (vector score wins over floor), got %v", res.Picked[0].Score)
	}
	if res.Picked[0].Source != PickSourceVector {
		t.Errorf("source should remain vector, got %q", res.Picked[0].Source)
	}
}

func TestPicker_BudgetTrimming_DropsLowestScore(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	steps := []models.ExplorationStep{
		makeStep(1, "X", "p1"),
		makeStep(2, "X", "p2"),
		makeStep(3, "X", "p3"),
	}
	hits := []RunStepIndexHit{
		{Step: 1, Score: 0.9},
		{Step: 2, Score: 0.8},
		{Step: 3, Score: 0.7},
	}
	// Estimate that returns a value above budget when more than 2 steps remain.
	estimate := func(s []models.ExplorationStep) int {
		// Each step adds 100 chars worth of "render".
		return len(s) * 100
	}
	// Budget allows 2 steps' worth (200 chars / 4 chars-per-token = 50 tokens).
	picker := &AnalysisStepPicker{
		Search:               searchFn(hits, nil),
		EstimateRenderedSize: estimate,
		BudgetTokens:         50,
	}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 2 {
		t.Fatalf("picked: got %d want 2 after budget trim", len(res.Picked))
	}
	if res.Picked[0].Step.Step != 1 || res.Picked[1].Step.Step != 2 {
		t.Errorf("picked must keep top 2 by score: got [%d %d]",
			res.Picked[0].Step.Step, res.Picked[1].Step.Step)
	}
	if len(res.Dropped) != 1 || res.Dropped[0].StepNumber != 3 {
		t.Errorf("dropped: want [{3, over_budget}], got %+v", res.Dropped)
	}
	if res.Dropped[0].Reason != DropReasonOverBudget {
		t.Errorf("drop reason: got %q want %q", res.Dropped[0].Reason, DropReasonOverBudget)
	}
}

func TestPicker_BudgetTrimming_KeepsAtLeastOne(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	steps := []models.ExplorationStep{makeStep(1, "X", "p1"), makeStep(2, "X", "p2")}
	hits := []RunStepIndexHit{
		{Step: 1, Score: 0.9},
		{Step: 2, Score: 0.8},
	}
	picker := &AnalysisStepPicker{
		Search:               searchFn(hits, nil),
		EstimateRenderedSize: func(s []models.ExplorationStep) int { return 99999 }, // always over
		BudgetTokens:         1,
	}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 1 {
		t.Errorf("picker must keep at least one step even when always over budget; got %d", len(res.Picked))
	}
}

func TestPicker_AreaWithEmptyKeywords_StillRanks(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X", Keywords: nil}
	steps := []models.ExplorationStep{makeStep(1, "X", "p1")}
	hits := []RunStepIndexHit{{Step: 1, Score: 0.9}}
	picker := &AnalysisStepPicker{Search: searchFn(hits, nil)}
	res, err := picker.Pick(context.Background(), area, steps)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if len(res.Picked) != 1 {
		t.Errorf("picked: got %d want 1", len(res.Picked))
	}
}

func TestPicker_AllStepsBelowMinScore_ReturnsEmpty(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	steps := []models.ExplorationStep{makeStep(1, "X", "p1")}
	hits := []RunStepIndexHit{{Step: 1, Score: 0.1}}
	picker := &AnalysisStepPicker{Search: searchFn(hits, nil)}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 0 {
		t.Errorf("picked: got %d want 0 (all below threshold)", len(res.Picked))
	}
	if len(res.Dropped) != 1 || res.Dropped[0].Reason != DropReasonBelowMinScore {
		t.Errorf("dropped: want one below_min_score, got %+v", res.Dropped)
	}
}

func TestPicker_DuplicateScoreOrdering_StableByStepNumber(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	steps := []models.ExplorationStep{
		makeStep(5, "X", "p5"),
		makeStep(2, "X", "p2"),
	}
	hits := []RunStepIndexHit{
		{Step: 5, Score: 0.6},
		{Step: 2, Score: 0.6},
	}
	picker := &AnalysisStepPicker{Search: searchFn(hits, nil)}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 2 {
		t.Fatalf("picked: got %d want 2", len(res.Picked))
	}
	if res.Picked[0].Step.Step != 2 || res.Picked[1].Step.Step != 5 {
		t.Errorf("tie-break order: got [%d %d] want [2 5]",
			res.Picked[0].Step.Step, res.Picked[1].Step.Step)
	}
}

func TestPicker_PropagatesSearchError(t *testing.T) {
	area := AnalysisArea{ID: "x", Name: "X"}
	picker := &AnalysisStepPicker{Search: searchFn(nil, errors.New("vector store down"))}
	if _, err := picker.Pick(context.Background(), area, nil); err == nil {
		t.Errorf("expected error from search")
	}
}

func TestPicker_RejectsNilSearch(t *testing.T) {
	picker := &AnalysisStepPicker{}
	if _, err := picker.Pick(context.Background(), AnalysisArea{}, nil); err == nil {
		t.Errorf("expected error for nil search")
	}
}

func TestBuildAreaQueryText(t *testing.T) {
	area := AnalysisArea{
		Name:        "Churn Risks",
		Description: "Where users disengage",
		Keywords:    []string{"retention", "session_count"},
	}
	got := buildAreaQueryText(area)
	wantParts := []string{"Churn Risks", "Where users disengage", "retention", "session_count"}
	for _, p := range wantParts {
		if !strings.Contains(got, p) {
			t.Errorf("buildAreaQueryText missing %q in %q", p, got)
		}
	}
}

func TestNewAnalysisStepPicker_DefaultRenderer(t *testing.T) {
	p := NewAnalysisStepPicker(searchFn(nil, nil))
	if p.EstimateRenderedSize == nil {
		t.Errorf("default constructor must wire EstimateRenderedSize")
	}
}

func TestPicker_ExactMatchBoost_SkipsBlankKeyword(t *testing.T) {
	// Blank keyword must not match every step ("" is a substring of
	// anything). The picker trims whitespace and skips empties.
	area := AnalysisArea{ID: "x", Name: "X", Keywords: []string{"", "  ", "real_term"}}
	steps := []models.ExplorationStep{
		makeStep(1, "noise", "irrelevant"),
		makeStep(2, "covers real_term well", "p2"),
	}
	picker := &AnalysisStepPicker{Search: searchFn(nil, nil)}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 1 || res.Picked[0].Step.Step != 2 {
		t.Errorf("blank keywords must not match; expected only step 2, got %+v", res.Picked)
	}
}

func TestPicker_PhantomHitFromIndexIgnored(t *testing.T) {
	// Index returns a step number that the orchestrator did not
	// supply (e.g. a stale index). The picker must skip it silently
	// rather than crashing or surfacing a phantom step.
	area := AnalysisArea{ID: "x", Name: "X"}
	steps := []models.ExplorationStep{makeStep(1, "X", "p1")}
	hits := []RunStepIndexHit{
		{Step: 999, Score: 0.9}, // phantom
		{Step: 1, Score: 0.7},
	}
	picker := &AnalysisStepPicker{Search: searchFn(hits, nil)}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 1 || res.Picked[0].Step.Step != 1 {
		t.Errorf("phantom hit must be dropped silently, got %+v", res.Picked)
	}
}

func TestPicker_ExactMatchOnLowVectorScoreBumpsToFloor(t *testing.T) {
	// Step is in the vector hits with a low score; exact-match logic
	// must promote its score to ExactMatchFloor (when below) without
	// changing source.
	area := AnalysisArea{ID: "x", Name: "X", Keywords: []string{"churn"}}
	steps := []models.ExplorationStep{makeStep(1, "churn rate", "p1")}
	hits := []RunStepIndexHit{{Step: 1, Score: 0.4}}
	picker := &AnalysisStepPicker{Search: searchFn(hits, nil)}
	res, _ := picker.Pick(context.Background(), area, steps)
	if len(res.Picked) != 1 {
		t.Fatalf("picked: got %d want 1", len(res.Picked))
	}
	if res.Picked[0].Score != ExactMatchFloor {
		t.Errorf("score must be promoted to ExactMatchFloor=%v, got %v", ExactMatchFloor, res.Picked[0].Score)
	}
}

func TestPicker_DefaultsAppliedWhenZero(t *testing.T) {
	// Zero values for TopK/MinScore/Budget must fall back to defaults.
	captured := RunStepIndexSearchOpts{}
	search := func(_ context.Context, _ string, opts RunStepIndexSearchOpts) ([]RunStepIndexHit, error) {
		captured = opts
		return nil, nil
	}
	picker := &AnalysisStepPicker{Search: search}
	if _, err := picker.Pick(context.Background(), AnalysisArea{}, nil); err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if captured.TopK != AnalysisAreaTopK {
		t.Errorf("default TopK: got %d want %d", captured.TopK, AnalysisAreaTopK)
	}
}
