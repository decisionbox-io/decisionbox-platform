package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// A recommendation reference is two ids, and both are held to the id grammar.
//
// A malformed half is bad input, not a missing target: a live run emitted a reference with
// an empty figure id, the resolver treated it as "nothing to point at", and `{{f1}}` shipped
// to a reader twice.

// TestRecommendationFigures_AnEmptyReferenceIsTheRecommendationsFault is the second half
// of R5 of the run: the same recommendation emitted `refs: [{"figure": ""}]` for another
// figure and shipped "{{f1}}" twice.
//
// The marker shipping is correct -- there is no number, and a reader seeing "{{f1}}" knows
// something went wrong. What was wrong was the verdict: an empty id fell through to the
// figure lookup and reported "insight 8ac98428 declares no figure", blaming a document
// that declares eight for a reference that named none of them. A reader of the audit trail
// inherits whichever of those it says.
func TestRecommendationFigures_AnEmptyReferenceIsTheRecommendationsFault(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Description: "Win back the {{f1}} buyers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	v := recs[0].FigureVerdicts[0]
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable", v.Status)
	}
	if strings.Contains(v.Reason, "declares no figure") {
		t.Errorf("reason = %q, which blames the insight for a reference that named no figure", v.Reason)
	}
	if !strings.Contains(v.Reason, "figure id") {
		t.Errorf("reason = %q, want it to say the reference names no usable figure id", v.Reason)
	}
	// The reference ships visible rather than as a number, which is unchanged.
	if !strings.Contains(recs[0].Description, "{{f1}}") {
		t.Errorf("description = %q, want the unresolved reference left visible", recs[0].Description)
	}
}

// TestRecommendationFigures_AReferenceNamingNoInsightSaysSo.
//
// The other half of a reference. An empty insight id used to fall through to the index
// lookup and report "insight  is not among the insights this recommendation was given",
// which reads as a citation that went stale rather than a reference that named nothing.
func TestRecommendationFigures_AReferenceNamingNoInsightSaysSo(t *testing.T) {
	recs := []models.Recommendation{{
		Description: "Win back the {{f1}} buyers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref("", "f1")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{bandInsight()})

	v := recs[0].FigureVerdicts[0]
	if v.Status != models.FigureUndecidable {
		t.Fatalf("status = %q, want undecidable", v.Status)
	}
	if !strings.Contains(v.Reason, "names no insight") {
		t.Errorf("reason = %q, want it to say the reference names no insight", v.Reason)
	}
}

// TestRecommendationFigures_AnIdOutsideTheGrammarIsRefused.
//
// The renderer resolves `[A-Za-z][A-Za-z0-9_]*` and the evaluator requires it of a
// figure's own id. A reference is held to the same grammar, so the two halves of this
// layer cannot disagree about what an id is -- the defect that let `id: "revenue-total"`
// hold while its placeholder shipped.
func TestRecommendationFigures_AnIdOutsideTheGrammarIsRefused(t *testing.T) {
	ins := bandInsight()
	for _, bad := range []string{"revenue-total", "1f", " f1", "f1 "} {
		recs := []models.Recommendation{{
			Description: "Win back the {{f1}} buyers.",
			Figures: []models.Figure{{
				ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
				Refs: []models.FigureRef{ref(ins.ID, bad)},
			}},
		}}
		attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
		v := recs[0].FigureVerdicts[0]
		if v.Status != models.FigureUndecidable || !strings.Contains(v.Reason, "figure id") {
			t.Errorf("reference to %q gave %q (%s), want undecidable naming the id grammar", bad, v.Status, v.Reason)
		}
	}
}
