package discovery

import (
	"context"
	"strings"
	"testing"

	valmodels "github.com/decisionbox-io/decisionbox/libs/go-common/models/validation"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/ai"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
	"github.com/decisionbox-io/decisionbox/services/agent/internal/validation/verifier"
)

// ValidateSingleDoc is the operator's "re-validate just this one" path, used by
// agentserver/validate_doc.go against a FINISHED discovery. It had no test:
// the per-item pause work changed its two call lines (the validation helpers
// grew a return value), which is what put it in front of codecov and is how
// the gap surfaced.
//
// What is worth pinning is the contract the caller depends on, not the
// verifier's answer: exactly one result comes back, the matching slice entry
// is stamped in place so the caller can persist both shapes, and a doc id that
// names nothing is an error rather than a zero-valued pass.

func singleDocInput(kind valmodels.DocKind, id string) ValidateSingleDocInput {
	cfg := verifier.DefaultConfig()
	cfg.RefuterEnabled = true
	llm := &inFlightLLM{}
	return ValidateSingleDocInput{
		AIClient:       &ai.Client{},
		VerifierClient: llm,
		RefuterClient:  llm,
		Cfg:            cfg,
		Caps:           verifier.RunCaps{MaxInsightsPerRun: 4, MaxRecommendationsPerRun: 4, Concurrency: 1},
		Executor:       emptyExecutor{},
		DocKind:        kind,
		DocID:          id,
		Insights: []models.Insight{
			{ID: "i0", Name: "finding zero", AffectedCount: 10, SourceSteps: []int{1}, AnalysisArea: "a"},
			{ID: "i1", Name: "finding one", AffectedCount: 20, SourceSteps: []int{1}, AnalysisArea: "a"},
		},
		Recs: []models.Recommendation{
			{ID: "r0", Title: "do the thing", RelatedInsightIDs: []string{"i0"}},
		},
		Steps: []models.ExplorationStep{{Step: 1, QueryResult: []map[string]interface{}{{"x": 1}}}},
	}
}

func TestValidateSingleDoc_Insight(t *testing.T) {
	in := singleDocInput(valmodels.DocInsight, "i1")
	got, err := ValidateSingleDoc(context.Background(), in)
	if err != nil {
		t.Fatalf("ValidateSingleDoc: %v", err)
	}
	if got.InsightID != "i1" {
		t.Errorf("result is for %q, want i1 — the wrong document was validated", got.InsightID)
	}
	// Stamped in place on the slice the caller owns, which is the half the
	// caller cannot do for itself: it persists the embedded array as well as
	// the standalone result.
	if in.Insights[1].Validation == nil {
		t.Error("the target insight was not stamped, so the caller would persist a document with no verdict")
	}
	if in.Insights[0].Validation != nil {
		t.Error("a non-target insight was stamped; only the requested document may be touched")
	}
}

func TestValidateSingleDoc_Recommendation(t *testing.T) {
	in := singleDocInput(valmodels.DocRecommendation, "r0")
	got, err := ValidateSingleDoc(context.Background(), in)
	if err != nil {
		t.Fatalf("ValidateSingleDoc: %v", err)
	}
	if got.InsightID != "r0" {
		t.Errorf("result is for %q, want r0", got.InsightID)
	}
	if in.Recs[0].Validation == nil {
		t.Error("the target recommendation was not stamped in place")
	}
}

func TestValidateSingleDoc_Refusals(t *testing.T) {
	t.Run("an unknown insight id is an error, not an empty pass", func(t *testing.T) {
		in := singleDocInput(valmodels.DocInsight, "nope")
		if _, err := ValidateSingleDoc(context.Background(), in); err == nil {
			t.Fatal("want an error for a doc id that names nothing in the discovery")
		} else if !strings.Contains(err.Error(), "nope") {
			t.Errorf("the error should name the id it could not find; got %v", err)
		}
	})

	t.Run("an unknown recommendation id is an error", func(t *testing.T) {
		in := singleDocInput(valmodels.DocRecommendation, "nope")
		if _, err := ValidateSingleDoc(context.Background(), in); err == nil {
			t.Fatal("want an error for a doc id that names nothing in the discovery")
		}
	})

	t.Run("no AI client is refused rather than nil-dereferenced", func(t *testing.T) {
		in := singleDocInput(valmodels.DocInsight, "i0")
		in.AIClient = nil
		if _, err := ValidateSingleDoc(context.Background(), in); err == nil {
			t.Fatal("want an error when no AI client is wired")
		}
	})
}
