package discovery

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// recordingCheckpoint is a perItemCheckpoint that remembers what it was asked
// to persist. The mutex matters: save is called from the bounded workers.
type recordingCheckpoint struct {
	mu    sync.Mutex
	saved []models.ValidationResult
	done  map[string]models.ValidationResult
}

func (r *recordingCheckpoint) hook() *perItemCheckpoint {
	return &perItemCheckpoint{
		done: r.done,
		save: func(vr models.ValidationResult) {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.saved = append(r.saved, vr)
		},
	}
}

func (r *recordingCheckpoint) savedIDs() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.saved))
	for _, vr := range r.saved {
		out[vr.InsightID] = true
	}
	return out
}

// priorVerdict is a verdict as a previous attempt's checkpoint holds it:
// fully formed, with its own timestamp and token counts, which is what proves
// a restore uses the stored values rather than re-deriving them.
func priorVerdict(id, area string, at time.Time) models.ValidationResult {
	v := &valmodels.StructuredVerdict{
		DocID:         id,
		DocKind:       valmodels.DocInsight,
		Mode:          valmodels.ModeVerifier,
		Overall:       valmodels.StatusConfirmed,
		OverallReason: "the rows say so",
		LLMTokensIn:   1234,
		LLMTokensOut:  56,
	}
	return models.ValidationResult{
		InsightID:     id,
		AnalysisArea:  area,
		ClaimedMetric: "finding " + id,
		ClaimedCount:  999,
		VerifiedCount: 999,
		ValidatedAt:   at,
		DocKind:       valmodels.DocInsight,
		WarehouseID:   "wh-secondary",
		Verifier:      v,
		Combined:      valmodels.StatusConfirmed,
		Status:        string(valmodels.StatusConfirmed),
		InputTokens:   1234,
		OutputTokens:  56,
	}
}

// TestValidateInsights_SkipsTheInsightsAPreviousAttemptAlreadyValidated is
// the acceptance criterion of the whole feature, at the seam that implements
// it: a run killed after validating J of M insights validates only M−J.
func TestValidateInsights_SkipsTheInsightsAPreviousAttemptAlreadyValidated(t *testing.T) {
	p, llm, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxInsightsPerRun = 6 // take the cap out of the picture

	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	cp := &recordingCheckpoint{done: map[string]models.ValidationResult{
		"i5": priorVerdict("i5", "a", at),
		"i4": priorVerdict("i4", "a", at),
	}}

	results, validated := p.validateInsights(context.Background(), insights, steps, "a", 0, cp.hook())

	if len(results) != 6 {
		t.Fatalf("want a result per insight, got %d", len(results))
	}
	// 6 insights, 2 restored, 4 checked — and each check is a verifier plus a
	// refuter, so 8 calls rather than 12.
	if llm.peak == 0 {
		t.Fatal("no LLM calls at all; the fixture is not exercising the verifier")
	}
	if got := cp.savedIDs(); len(got) != 4 {
		t.Errorf("persisted %d verdicts, want 4 — the restored two must not be re-saved", len(got))
	} else if got["i5"] || got["i4"] {
		t.Error("re-saved a verdict a previous attempt already paid for")
	}

	// The budget counts the restored ones: the previous attempt spent that
	// slice of the cap, and a resume that reset it would validate past the
	// number the project set.
	if validated != 6 {
		t.Errorf("runValidated = %d, want 6 (2 restored + 4 checked)", validated)
	}

	// The restored verdicts come back verbatim, in their own slots, and are
	// stamped onto the insights.
	byID := map[string]models.ValidationResult{}
	for _, r := range results {
		byID[r.InsightID] = r
	}
	for _, id := range []string{"i5", "i4"} {
		got := byID[id]
		if !got.ValidatedAt.Equal(at) {
			t.Errorf("%s validated_at = %v, want the stored %v — a restore must not restamp when the work happened", id, got.ValidatedAt, at)
		}
		if got.WarehouseID != "wh-secondary" {
			t.Errorf("%s warehouse_id = %q, want the stored one", id, got.WarehouseID)
		}
		if got.InputTokens != 1234 || got.OutputTokens != 56 {
			t.Errorf("%s lost its token accounting: %d/%d", id, got.InputTokens, got.OutputTokens)
		}
		if got.Combined != valmodels.StatusConfirmed {
			t.Errorf("%s combined = %q, want the stored confirmed", id, got.Combined)
		}
	}
	for i := range insights {
		if insights[i].Validation == nil {
			t.Fatalf("%s left with no Validation — filterEligibleInsights would fail it open into the recommender", insights[i].ID)
		}
	}
	for _, id := range []string{"i5", "i4"} {
		var ins *models.Insight
		for i := range insights {
			if insights[i].ID == id {
				ins = &insights[i]
			}
		}
		if ins.Validation.Combined != valmodels.StatusConfirmed {
			t.Errorf("%s stamped %q, want the restored confirmed", id, ins.Validation.Combined)
		}
		if ins.Validation.Reasoning != "the rows say so" {
			t.Errorf("%s lost the restored reasoning", id)
		}
	}
}

// TestValidateInsights_RestoredMarkersSpendTheCapSoItIsNeverExceeded — the
// cap is a per-RUN budget threaded across areas, so a resumed run that reset
// it would validate past the number the project set.
func TestValidateInsights_RestoredMarkersSpendTheCapSoItIsNeverExceeded(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxInsightsPerRun = 4

	at := time.Now().Add(-time.Hour)
	cp := &recordingCheckpoint{done: map[string]models.ValidationResult{
		"i5": priorVerdict("i5", "a", at),
		"i4": priorVerdict("i4", "a", at),
		"i3": priorVerdict("i3", "a", at),
	}}
	results, validated := p.validateInsights(context.Background(), insights, steps, "a", 0, cp.hook())

	if validated != 4 {
		t.Errorf("runValidated = %d, want exactly the cap of 4", validated)
	}
	if got := len(cp.savedIDs()); got != 1 {
		t.Errorf("checked %d insights, want 1 — three were restored and the cap allows four", got)
	}
	skipped := 0
	for _, r := range results {
		if r.Combined == valmodels.StatusSkippedBudgetCap {
			skipped++
		}
	}
	if skipped != 2 {
		t.Errorf("%d insights skipped on the cap, want 2", skipped)
	}
}

// TestValidateInsights_ARaisedCapValidatesWhatThePreviousAttemptSkipped — the
// cap is re-decided over whatever is left, so raising
// VALIDATION_MAX_INSIGHTS_PER_RUN between attempts takes effect rather than
// being frozen by the restore.
func TestValidateInsights_ARaisedCapValidatesWhatThePreviousAttemptSkipped(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxInsightsPerRun = 6 // raised from the 4 the previous attempt ran under

	at := time.Now().Add(-time.Hour)
	cp := &recordingCheckpoint{done: map[string]models.ValidationResult{
		"i5": priorVerdict("i5", "a", at),
		"i4": priorVerdict("i4", "a", at),
		"i3": priorVerdict("i3", "a", at),
		"i2": priorVerdict("i2", "a", at),
	}}
	results, validated := p.validateInsights(context.Background(), insights, steps, "a", 0, cp.hook())

	if validated != 6 {
		t.Errorf("runValidated = %d, want 6", validated)
	}
	if got := len(cp.savedIDs()); got != 2 {
		t.Errorf("checked %d insights, want the 2 the previous attempt had skipped on the cap", got)
	}
	for _, r := range results {
		if r.Combined == valmodels.StatusSkippedBudgetCap {
			t.Error("still skipping on a cap that was raised")
		}
	}
}

// TestValidateInsights_PersistsEveryCheckedVerdictUnderConcurrency — the
// markers are written from the bounded workers, so every submitted document
// must have landed by the time the phase returns. If it had not, an area that
// completed would leave markers racing its own completed checkpoint.
func TestValidateInsights_PersistsEveryCheckedVerdictUnderConcurrency(t *testing.T) {
	p, llm, insights, steps := concurrencyFixture(t, 3)
	p.caps.MaxInsightsPerRun = 6
	cp := &recordingCheckpoint{}

	results, _ := p.validateInsights(context.Background(), insights, steps, "a", 0, cp.hook())

	if llm.peak < 2 {
		t.Fatalf("peak concurrency %d; this test is not exercising the parallel path", llm.peak)
	}
	saved := cp.savedIDs()
	if len(saved) != len(results) {
		t.Fatalf("persisted %d verdicts for %d results — a marker landed after the phase returned", len(saved), len(results))
	}
	for _, r := range results {
		if !saved[r.InsightID] {
			t.Errorf("no marker for %s", r.InsightID)
		}
	}
}

// TestValidateInsights_DoesNotPersistACapSkippedVerdict — a cap-skipped
// insight cost nothing, so recording it would make len(InsightVerdicts) a
// wrong budget and a later attempt would refuse to validate it.
func TestValidateInsights_DoesNotPersistACapSkippedVerdict(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxInsightsPerRun = 2
	cp := &recordingCheckpoint{}

	p.validateInsights(context.Background(), insights, steps, "a", 0, cp.hook())

	if got := len(cp.savedIDs()); got != 2 {
		t.Errorf("persisted %d markers, want 2 — the four cap-skipped insights cost nothing and must not be marked", got)
	}
}

// TestValidateInsights_NilCheckpointIsTheOldPath — a deployment with no store
// must behave exactly as it always did.
func TestValidateInsights_NilCheckpointIsTheOldPath(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	results, validated := p.validateInsights(context.Background(), insights, steps, "a", 0, nil)
	if len(results) != 6 || validated != 4 {
		t.Errorf("results = %d, validated = %d; want 6 and the cap of 4", len(results), validated)
	}
}

// TestValidateRecommendations_SkipsTheOnesAlreadyValidated — the same
// acceptance criterion for Phase 5.5, including the cap.
func TestValidateRecommendations_SkipsTheOnesAlreadyValidated(t *testing.T) {
	p, _, insights, steps := concurrencyFixture(t, 1)
	p.caps.MaxRecommendationsPerRun = 4

	recs := make([]models.Recommendation, 5)
	for i := range recs {
		recs[i] = models.Recommendation{
			ID:                fmt.Sprintf("r%d", i),
			Title:             fmt.Sprintf("do thing %d", i),
			RelatedInsightIDs: []string{insights[0].ID},
		}
	}
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	prior := func(id string) models.ValidationResult {
		vr := priorVerdict(id, "", at)
		vr.DocKind = valmodels.DocRecommendation
		vr.Verifier.DocKind = valmodels.DocRecommendation
		return vr
	}
	cp := &recordingCheckpoint{done: map[string]models.ValidationResult{
		"r0": prior("r0"),
		"r1": prior("r1"),
	}}

	results := p.validateRecommendations(context.Background(), recs, insights, steps, cp.hook())

	if len(results) != 5 {
		t.Fatalf("want a result per recommendation, got %d", len(results))
	}
	saved := cp.savedIDs()
	if len(saved) != 2 {
		t.Errorf("checked %d recommendations, want 2 — two were restored and the cap allows four", len(saved))
	}
	if saved["r0"] || saved["r1"] {
		t.Error("re-saved a verdict a previous attempt already paid for")
	}
	skipped := 0
	for _, r := range results {
		if r.Combined == valmodels.StatusSkippedBudgetCap {
			skipped++
		}
	}
	if skipped != 1 {
		t.Errorf("%d recommendations skipped on the cap, want 1", skipped)
	}
	for i := range recs {
		if recs[i].Validation == nil {
			t.Errorf("%s left with no Validation", recs[i].ID)
		}
	}
	if recs[0].Validation.ValidatedAt.Equal(at) != true {
		t.Error("a restored recommendation was restamped with this attempt's time")
	}
}

// TestInsightValidationFrom_RendersWhatTheLivePathUsedToBuildInline is the
// anti-drift guard. The ValidationResult is what gets checkpointed, so a
// second construction of this struct anywhere would be a second definition of
// what a stored verdict means.
func TestInsightValidationFrom_RendersWhatTheLivePathUsedToBuildInline(t *testing.T) {
	verifierVerdict := &valmodels.StructuredVerdict{
		DocID:         "i1",
		Mode:          valmodels.ModeVerifier,
		Overall:       valmodels.StatusSupported,
		OverallReason: "rows agree",
	}
	refuterVerdict := &valmodels.StructuredVerdict{DocID: "i1", Mode: valmodels.ModeRefuter}
	at := time.Now()

	t.Run("an insight carries the legacy Status and Reasoning", func(t *testing.T) {
		vr := models.ValidationResult{
			DocKind: valmodels.DocInsight, WarehouseID: "wh",
			Verifier: verifierVerdict, Refuter: refuterVerdict,
			Combined: valmodels.StatusSupported, ValidatedAt: at,
			InputTokens: 10, OutputTokens: 2,
		}
		got := insightValidationFrom(vr)
		if got.Status != string(valmodels.StatusSupported) {
			t.Errorf("Status = %q, want the legacy backfill consumers still read", got.Status)
		}
		if got.Reasoning != "rows agree" {
			t.Errorf("Reasoning = %q, want the verifier's overall reason", got.Reasoning)
		}
		if got.WarehouseID != "wh" || got.Combined != valmodels.StatusSupported ||
			!got.ValidatedAt.Equal(at) || got.InputTokens != 10 || got.OutputTokens != 2 {
			t.Errorf("lost a field: %+v", got)
		}
		if got.Verifier != verifierVerdict || got.Refuter != refuterVerdict {
			t.Error("did not carry both verdicts through")
		}
	})

	t.Run("a recommendation does not", func(t *testing.T) {
		// Recommendation validations have never carried the legacy pair.
		// Widening that here would be an unrequested change to what the
		// dashboard renders.
		vr := models.ValidationResult{
			DocKind: valmodels.DocRecommendation,
			Verifier: verifierVerdict, Combined: valmodels.StatusSupported,
		}
		got := insightValidationFrom(vr)
		if got.Status != "" || got.Reasoning != "" {
			t.Errorf("Status = %q Reasoning = %q, want both empty for a recommendation", got.Status, got.Reasoning)
		}
		if got.Combined != valmodels.StatusSupported {
			t.Error("lost the verdict itself")
		}
	})

	t.Run("a verdict with no verifier does not panic", func(t *testing.T) {
		// Reachable: a cap-skipped document's result carries no verdicts.
		got := insightValidationFrom(models.ValidationResult{
			DocKind: valmodels.DocInsight, Combined: valmodels.StatusSkippedBudgetCap,
		})
		if got.Reasoning != "" {
			t.Errorf("Reasoning = %q, want empty", got.Reasoning)
		}
	})
}

// TestPerItemCheckpoint_IsNilSafe — every call site may pass nil, and
// validate_single_doc.go does.
func TestPerItemCheckpoint_IsNilSafe(t *testing.T) {
	var cp *perItemCheckpoint
	if _, ok := cp.restored("i1"); ok {
		t.Error("nil checkpoint reported a restored verdict")
	}
	cp.record(models.ValidationResult{InsightID: "i1"})

	empty := &perItemCheckpoint{}
	if _, ok := empty.restored("i1"); ok {
		t.Error("an empty checkpoint reported a restored verdict")
	}
	empty.record(models.ValidationResult{InsightID: "i1"})

	// An empty document id must never match, or a document with no id would
	// pick up another's verdict.
	withDone := &perItemCheckpoint{done: map[string]models.ValidationResult{"": {InsightID: "ghost"}}}
	if _, ok := withDone.restored(""); ok {
		t.Error("matched an empty document id")
	}
}

// TestValidateInsights_ValidationDisabledIgnoresTheCheckpoint — with no agent
// the phase stamps validation_disabled and makes no calls, so there is
// nothing to restore and nothing to record.
func TestValidateInsights_ValidationDisabledIgnoresTheCheckpoint(t *testing.T) {
	p := &validationPhase{caps: verifier.RunCaps{MaxInsightsPerRun: 10}}
	insights := []models.Insight{{ID: "i1"}}
	cp := &recordingCheckpoint{done: map[string]models.ValidationResult{
		"i1": priorVerdict("i1", "a", time.Now()),
	}}
	results, validated := p.validateInsights(context.Background(), insights, nil, "a", 0, cp.hook())

	if len(results) != 0 || validated != 0 {
		t.Errorf("results = %d validated = %d, want 0 and 0", len(results), validated)
	}
	if insights[0].Validation.Combined != valmodels.StatusValidationDisabled {
		t.Errorf("combined = %q, want validation_disabled", insights[0].Validation.Combined)
	}
	if len(cp.savedIDs()) != 0 {
		t.Error("recorded a marker for a phase that made no calls")
	}
}
