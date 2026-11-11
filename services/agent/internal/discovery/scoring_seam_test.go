package discovery

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// What this file pins: WHERE the scorer runs inside the pipeline.
//
// Both seams are load-bearing in a way a unit test on the scorer itself cannot
// see, so these read the orchestrator's source and assert the order of the
// calls. That is the honest shape of the claim: the invariant is positional,
// and the alternative — driving a full RunDiscovery to observe it — would
// prove the same thing through twenty collaborators that have nothing to do
// with it.
const orchestratorSource = "orchestrator.go"

// callOrder returns the byte offsets at which each named call appears in the
// orchestrator's source, failing if any appears other than exactly once.
func callOrder(t *testing.T, calls ...string) []int {
	t.Helper()
	src, err := os.ReadFile(orchestratorSource)
	if err != nil {
		t.Fatalf("reading %s: %v", orchestratorSource, err)
	}
	text := string(src)
	out := make([]int, 0, len(calls))
	for _, call := range calls {
		if n := strings.Count(text, call); n != 1 {
			t.Fatalf("%s appears %d times in %s, want exactly once — this guard reads positions and "+
				"cannot be trusted with two of them", call, n, orchestratorSource)
		}
		out = append(out, strings.Index(text, call))
	}
	return out
}

// TestTheInsightScorerRunsAfterTheQualityLabelAndBeforeTheCheckpoint pins seam
// one, which is constrained from both sides.
//
// After attachSourceQuality, because one of the deductions reads the caveat it
// attaches — score first and a degraded insight scores as a clean one.
//
// Before checkpointAnalysisArea, because a completed area is restored with
// `continue` at the top of the area loop and never re-enters the body. A score
// computed after the checkpoint write would therefore be absent from every
// restored area on every resumed run: the band would exist on a fresh run and
// not on a resumed one, which is the worst shape for this bug to take because
// resume is exactly when nobody is watching.
func TestTheInsightScorerRunsAfterTheQualityLabelAndBeforeTheCheckpoint(t *testing.T) {
	pos := callOrder(t,
		"attachSourceQuality(insights, stepByID)",
		"scoreInsights(insights, o.scoringCfg)",
		"o.checkpointAnalysisArea(ctx, step, areaSpend)",
	)
	if !(pos[0] < pos[1]) {
		t.Error("the scorer runs BEFORE attachSourceQuality, so a degraded insight is scored as a clean one")
	}
	if !(pos[1] < pos[2]) {
		t.Error("the scorer runs AFTER checkpointAnalysisArea, so every area restored on a resumed run " +
			"comes back unscored and renders as legacy")
	}
}

// TestTheRecommendationScorerRunsAfterTheRemint pins seam two.
//
// After reidentifyRestoredRecommendations, because a restored recommendation's
// ids are only safe from that point — and the scored recommendations are what
// the tail writes under those ids.
func TestTheRecommendationScorerRunsAfterTheRemint(t *testing.T) {
	pos := callOrder(t,
		"reidentifyRestoredRecommendations(recommendations, recValidationResults)",
		"scoreRecommendations(recommendations, o.scoringCfg)",
	)
	if !(pos[0] < pos[1]) {
		t.Error("the recommendation scorer runs before the remint, so a restored recommendation is " +
			"scored under an id the tail will not write")
	}
}

// TestARestoredAreaComesBackScored is the behavioural half of seam one: the
// score rides inside the checkpoint's own analysis step, so restoring the area
// restores the grade with it. No checkpoint schema change, and nothing
// re-scores — which also means no dock is charged twice.
func TestARestoredAreaComesBackScored(t *testing.T) {
	now := time.Now()
	stored := doneArea("churn", 2, now.Add(-time.Hour))
	// As the loop body leaves it: scored before the checkpoint was written.
	scoreInsights(stored.Insights, testScoringCfg())
	want := *stored.Insights[0].Scoring

	log, insights, _ := restoreArea(
		models.AnalysisAreaCheckpoint{Step: stored, InsightsValidated: 2},
		nil, nil,
	)

	if len(insights) != 2 {
		t.Fatalf("got %d insights, want 2", len(insights))
	}
	for i := range insights {
		if insights[i].Scoring == nil {
			t.Fatalf("restored insight %d came back unscored, so it would render as legacy", i)
		}
		if !reflect.DeepEqual(*insights[i].Scoring, want) {
			t.Errorf("restored insight %d was re-scored or altered:\n got  = %+v\n want = %+v",
				i, *insights[i].Scoring, want)
		}
		if insights[i].Confidence != want.ConfidenceScore {
			t.Errorf("restored insight %d confidence = %v, want %v",
				i, insights[i].Confidence, want.ConfidenceScore)
		}
	}
	// And the analysis log's copy agrees, because the checkpoint is what the
	// split logs are rebuilt from.
	if log[0].Insights[0].Scoring == nil || !reflect.DeepEqual(*log[0].Insights[0].Scoring, want) {
		t.Errorf("the restored analysis step's insight lost its score: %+v", log[0].Insights[0].Scoring)
	}
}

// TestADraftRestoredAreaIsScoredOnTheAttemptThatFinishesIt — a draft carries
// insights that have been analysed but not fully validated, so its score is
// not final. The attempt that finishes the area's validation runs the scorer
// over them, and the verdict banked in the meantime is what it scores.
func TestADraftRestoredAreaIsScoredOnTheAttemptThatFinishesIt(t *testing.T) {
	now := time.Now()
	draft := doneArea("churn", 1, now)
	// A draft is written before validation finishes, so its insight carries no
	// verdict yet and no score at all.
	draft.Insights[0].Validation = nil
	draft.Insights[0].Scoring = nil
	draft.Insights[0].Confidence = 0.91 // ...but it does carry the model's guess

	// The resumed attempt validates the remainder, which stamps the verdict.
	draft.Insights[0].Validation = &models.InsightValidation{Combined: valmodels.StatusPartial}

	scoreInsights(draft.Insights, testScoringCfg())

	got := draft.Insights[0]
	if got.Scoring == nil {
		t.Fatal("the draft-restored insight was left unscored")
	}
	if got.Scoring.ConfidenceBand != models.ConfidenceBandMedium || got.Scoring.ConfidenceScore != 0.55 {
		t.Errorf("band/score = %q/%v, want medium/0.55 for a partial verdict",
			got.Scoring.ConfidenceBand, got.Scoring.ConfidenceScore)
	}
	if got.Confidence != 0.55 {
		t.Errorf("confidence = %v, want the derived 0.55 — the draft's 0.91 survived", got.Confidence)
	}
}
