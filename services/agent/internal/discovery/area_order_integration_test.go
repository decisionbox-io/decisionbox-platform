//go:build integration

package discovery

import (
	"context"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/database"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The area order rests on one claim about the repository: that ListRecent's
// projection carries each insight's VALIDATION state, not only its name and
// area. That is a projection detail no unit test can prove, so it is proven
// here against a real Mongo and the real repository — seeding two discoveries,
// reading them back through the same call the run makes, and asserting both
// the ages and that this run's own result is skipped.
func TestInteg_AreaOrder_StalenessComesFromTheStoredHistory(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()

	repo := database.NewDiscoveryRepository(db)
	finding := func(area string, state valmodels.Status) models.Insight {
		return models.Insight{
			ID:           area + "-" + string(state),
			AnalysisArea: area,
			Name:         "finding in " + area,
			Validation:   &models.InsightValidation{Combined: state},
		}
	}

	// The project's last complete run: two areas went unjudged when the
	// budget ran out, one was folded as a restatement, one was validated.
	previous := &models.DiscoveryResult{
		ProjectID:     "proj",
		RunID:         "run-previous",
		DiscoveryDate: time.Now().Add(-2 * time.Hour),
		Insights: []models.Insight{
			finding("stock", valmodels.StatusSkippedBudgetCap),
			finding("margin", valmodels.StatusSkippedBudgetCap),
			finding("traffic", valmodels.StatusSkippedDuplicate),
			finding("vendors", valmodels.StatusSupported),
		},
	}
	// And this run's own partial result, newer, which must not be read: its
	// starvation describes how far the attempt that crashed got.
	mine := &models.DiscoveryResult{
		ProjectID:     "proj",
		RunID:         "run-mine",
		DiscoveryDate: time.Now(),
		Insights: []models.Insight{
			finding("vendors", valmodels.StatusSkippedBudgetCap),
		},
	}
	for _, d := range []*models.DiscoveryResult{previous, mine} {
		if err := repo.Save(ctx, d); err != nil {
			t.Fatalf("seed %s: %v", d.RunID, err)
		}
	}

	// Through the same repository call the run makes, then the same selection
	// the run applies to its result.
	recent, err := repo.ListRecent(ctx, "proj", 3)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	got := validationStaleness(recent, "run-mine")

	// One usable discovery, so an area it judged is age 0 and an area it
	// capped is as stale as the window is long.
	if got["traffic"] != 0 || got["vendors"] != 0 {
		t.Errorf("traffic=%d vendors=%d, want 0 — both were answered in the previous run", got["traffic"], got["vendors"])
	}
	if _, ranked := got["stock"]; ranked {
		t.Errorf("stock is ranked %d; a capped area was never judged and must fall to the window length", got["stock"])
	}
	if got[unjudgedArea] != 1 {
		t.Errorf("window = %d, want 1 usable discovery — this run's own partial result does not count", got[unjudgedArea])
	}

	// And the order the run works through: the two areas the previous run
	// could not check first, the rest in the id order resolvePrompts hands back.
	areas := []AnalysisArea{{ID: "margin"}, {ID: "stock"}, {ID: "traffic"}, {ID: "vendors"}}
	ordered := areaIDs(orderAreasStalestFirst(areas, got))
	for i, want := range []string{"margin", "stock", "traffic", "vendors"} {
		if ordered[i] != want {
			t.Fatalf("order = %v, want the two unchecked areas first, each group in id order", ordered)
		}
	}
}
