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

// The starved-area order rests on one claim about the repository: that
// ListRecent's projection carries each insight's VALIDATION state, not only its
// name and area. That is a projection detail no unit test can prove, so it is
// proven here against a real Mongo and the real repository — seeding two
// discoveries, reading them back through the same call the run makes, and
// asserting both the starved set and that this run's own result is skipped.
func TestInteg_AreaOrder_StarvedAreasComeFromThePreviousDiscovery(t *testing.T) {
	db, cleanup := analysisResumeMongo(t)
	defer cleanup()
	ctx := context.Background()

	repo := database.NewDiscoveryRepository(db)
	starved := func(area string, state valmodels.Status) models.Insight {
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
			starved("stock", valmodels.StatusSkippedBudgetCap),
			starved("margin", valmodels.StatusSkippedBudgetCap),
			starved("traffic", valmodels.StatusSkippedDuplicate),
			starved("vendors", valmodels.StatusSupported),
		},
	}
	// And this run's own partial result, newer, which must not be read: its
	// starvation describes how far the attempt that crashed got.
	mine := &models.DiscoveryResult{
		ProjectID:     "proj",
		RunID:         "run-mine",
		DiscoveryDate: time.Now(),
		Insights: []models.Insight{
			starved("vendors", valmodels.StatusSkippedBudgetCap),
		},
	}
	for _, d := range []*models.DiscoveryResult{previous, mine} {
		if err := repo.Save(ctx, d); err != nil {
			t.Fatalf("seed %s: %v", d.RunID, err)
		}
	}

	o := &Orchestrator{discoveryRepo: repo, projectID: "proj", runID: "run-mine"}
	got := o.previousRunStarvedAreas(ctx)

	if len(got) != 2 || !got["stock"] || !got["margin"] {
		t.Fatalf("starved = %v, want stock and margin from the previous run only", got)
	}
	if got["vendors"] {
		t.Error("read this run's own partial result: vendors was starved only there")
	}
	if got["traffic"] {
		t.Error("a folded restatement was counted as starved")
	}

	// And the order the run works through: the starved areas first, the rest
	// in the id order resolvePrompts hands back.
	areas := []AnalysisArea{{ID: "margin"}, {ID: "stock"}, {ID: "traffic"}, {ID: "vendors"}}
	ordered := areaIDs(orderAreasStarvedFirst(areas, got))
	for i, want := range []string{"margin", "stock", "traffic", "vendors"} {
		if ordered[i] != want {
			t.Fatalf("order = %v, want the two starved areas first, each group in id order", ordered)
		}
	}
}
