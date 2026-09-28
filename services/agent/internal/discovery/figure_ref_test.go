package discovery

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

// The cases here are the measured ones. Every scenario below is either a falsehood an
// adjudicated run actually shipped or a failure mode this layer's design deliberately
// chose, so a change that quietly gives one of them up fails here.

// bandInsight is the insight behind the one recommendation falsehood the figure layer on
// insights did not reach: order-count bands over all customers, every band's count
// checked and standing.
func bandInsight() models.Insight {
	return models.Insight{
		ID:   "11111111-2222-3333-4444-555555555555",
		Name: "Order frequency is concentrated in the middle bands",
		Figures: []models.Figure{
			{ID: "f1", Value: 52134, Unit: models.UnitCount, Step: 2, Kind: models.FigureCell, Column: "customers", Row: "band = 6-15"},
			{ID: "f2", Value: 43897, Unit: models.UnitCount, Step: 2, Kind: models.FigureCell, Column: "customers", Row: "band = 16-30"},
		},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f1", Status: models.FigureHolds, Claimed: 52134, Evaluated: 52134},
			{ID: "f2", Status: models.FigureHolds, Claimed: 43897, Evaluated: 43897},
		},
	}
}

func ref(insight, figure string) models.FigureRef {
	return models.FigureRef{Insight: insight, Figure: figure}
}

// TestRecommendationFigures_FabricatedTotalIsReplacedByTheReferences is s11 R5.
//
// The shipped headline read "Build a Retention Program for the 96,447 Buyers in the 6+
// Order Frequency Bands". The bands it named hold 52,134 and 43,897, which total 96,031 --
// and the recommendation's own body said 96,031 two paragraphs later. 96,447 matches no
// combination of the bands at all.
func TestRecommendationFigures_FabricatedTotalIsReplacedByTheReferences(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Title:       "Build a retention program for the {{f1}} buyers in the 6+ order frequency bands",
		Description: "{{f1}} buyers sit in the two highest frequency bands.",
		Figures: []models.Figure{{
			ID: "f1", Value: 96447, Unit: models.UnitCount, Kind: models.FigureSum,
			Refs: []models.FigureRef{ref(ins.ID, "f1"), ref(ins.ID, "f2")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if got := recs[0].Figures[0].Value; got != 96031 {
		t.Fatalf("figure value = %v, want the references' total 96031", got)
	}
	for _, field := range []string{recs[0].Title, recs[0].Description} {
		if !strings.Contains(field, "96,031") {
			t.Errorf("prose reads %q, want the resolved total 96,031", field)
		}
		if strings.Contains(field, "96,447") {
			t.Errorf("prose still carries the fabricated total: %q", field)
		}
	}
	if len(recs[0].FigureCorrections) != 1 {
		t.Fatalf("corrections = %d, want the replacement recorded", len(recs[0].FigureCorrections))
	}
	if c := recs[0].FigureCorrections[0]; c.From != 96447 || c.To != 96031 {
		t.Errorf("correction = %v -> %v, want 96447 -> 96031", c.From, c.To)
	}
	// And the verdict describes what will be read, not what was refuted.
	if s := recs[0].FigureVerdicts[0].Status; s != models.FigureHolds {
		t.Errorf("verdict after adoption = %q, want holds", s)
	}
}

// TestRecommendationFigures_TitleAndBodyCannotDisagree is the structural half of the same
// bug. Two mentions of one reference are one number rendered twice; there is no second
// place for a different digit to come from.
func TestRecommendationFigures_TitleAndBodyCannotDisagree(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Title:       "Win back {{f1}} buyers",
		Description: "The program targets {{f1}} buyers across both bands.",
		Actions:     []string{"Segment the {{f1}} buyers by recency"},
		ExpectedImpact: models.Impact{
			Reasoning: "Reactivating even a small share of {{f1}} buyers moves repeat frequency.",
		},
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureSum,
			Refs: []models.FigureRef{ref(ins.ID, "f1"), ref(ins.ID, "f2")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	rec := recs[0]
	fields := []string{rec.Title, rec.Description, rec.Actions[0], rec.ExpectedImpact.Reasoning}
	for _, f := range fields {
		if !strings.Contains(f, "96,031") {
			t.Errorf("field %q does not carry the one resolved value", f)
		}
	}
}

// TestRecommendationFigures_RefTakesTheInsightsCheckedValue is the restatement case, which
// is 94% of the numerals a recommendation writes. The model declares no value and cannot
// mistype one; s7 R4.4 and s8 R1.4 were both retyped numbers.
func TestRecommendationFigures_RefTakesTheInsightsCheckedValue(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Description: "The 6-15 band alone holds {{f1}} customers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f1")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if !strings.Contains(recs[0].Description, "52,134") {
		t.Errorf("description = %q, want the insight's checked value 52,134", recs[0].Description)
	}
}

// TestRecommendationFigures_RefRewritesNotationWithoutRetypingTheNumber covers the
// "notation variant" class: an insight wrote the amount in full, the recommendation wants
// it abbreviated. Measured cases include a recommendation writing "$3.63M" for a figure an
// insight wrote out, and "121 days" for one it wrote as 111.5.
func TestRecommendationFigures_RefRewritesNotationWithoutRetypingTheNumber(t *testing.T) {
	ins := models.Insight{
		ID:             "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Figures:        []models.Figure{{ID: "f1", Value: 8476238553, Unit: models.UnitCurrency, Decimals: 2}},
		FigureVerdicts: []models.FigureVerdict{{ID: "f1", Status: models.FigureHolds}},
	}
	recs := []models.Recommendation{{
		Description: "The decile contributes {{f1}} a year.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCurrency, Scale: models.ScaleBillions, Decimals: 1,
			Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(ins.ID, "f1")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if !strings.Contains(recs[0].Description, "$8.5B") {
		t.Errorf("description = %q, want $8.5B -- the insight's value in the recommendation's notation", recs[0].Description)
	}
}

// TestRecommendationFigures_UnusableOperandIsUndecidableNotRefuted holds the line the
// insight evaluator holds: an evaluator that reports its own limits as the document's
// errors costs more than it catches.
func TestRecommendationFigures_UnusableOperandIsUndecidableNotRefuted(t *testing.T) {
	refuted := models.Insight{
		ID:             "11111111-2222-3333-4444-555555555555",
		Figures:        []models.Figure{{ID: "f1", Value: 100000, Unit: models.UnitCount}},
		FigureVerdicts: []models.FigureVerdict{{ID: "f1", Status: models.FigureFails, Claimed: 100000, Evaluated: 99996}},
	}
	unchecked := models.Insight{
		ID:      "99999999-8888-7777-6666-555555555555",
		Figures: []models.Figure{{ID: "f1", Value: 42, Unit: models.UnitCount}},
	}

	cases := []struct {
		name string
		ins  models.Insight
		want string
	}{
		{"its own check failed", refuted, "came back fails"},
		{"never checked", unchecked, "never checked"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs := []models.Recommendation{{
				Description: "All {{f1}} buyers.",
				Figures: []models.Figure{{
					ID: "f1", Value: 100000, Unit: models.UnitCount, Kind: models.FigureRefKind,
					Refs: []models.FigureRef{ref(tc.ins.ID, "f1")},
				}},
			}}
			attachRecommendationFigureVerdicts(recs, []models.Insight{tc.ins})
			v := recs[0].FigureVerdicts[0]
			if v.Status != models.FigureUndecidable {
				t.Fatalf("status = %q, want undecidable -- a reference Go cannot stand behind says nothing about the number", v.Status)
			}
			if !strings.Contains(v.Reason, tc.want) {
				t.Errorf("reason = %q, want it to mention %q", v.Reason, tc.want)
			}
			if len(recs[0].FigureCorrections) != 0 {
				t.Errorf("an undecidable figure must not be corrected, got %v", recs[0].FigureCorrections)
			}
		})
	}
}

// TestRecommendationFigures_UnresolvableReferenceStillRendersSomething is the floor this
// design promises: on a resolution failure the declared value renders, so the prose is
// never left with a hole and the verdict says the number is unchecked. That is exactly
// what a number typed into prose is today -- the difference is the label.
func TestRecommendationFigures_UnresolvableReferenceStillRendersSomething(t *testing.T) {
	ins := bandInsight()
	cases := []struct {
		name string
		refs []models.FigureRef
		want string
	}{
		{"unknown insight", []models.FigureRef{ref("not-an-insight-id", "f1")}, "not among the insights"},
		{"unknown figure", []models.FigureRef{ref(ins.ID, "f9")}, "declares no figure f9"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recs := []models.Recommendation{{
				Description: "Target the {{f1}} buyers.",
				Figures: []models.Figure{{
					ID: "f1", Value: 96031, Unit: models.UnitCount,
					Kind: models.FigureRefKind, Refs: tc.refs,
				}},
			}}
			attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
			renderRecommendationFigures(recs)

			v := recs[0].FigureVerdicts[0]
			if v.Status != models.FigureUndecidable {
				t.Errorf("status = %q, want undecidable", v.Status)
			}
			if !strings.Contains(v.Reason, tc.want) {
				t.Errorf("reason = %q, want it to mention %q", v.Reason, tc.want)
			}
			if !strings.Contains(recs[0].Description, "96,031") {
				t.Errorf("description = %q, want the declared value rendered rather than a hole in the sentence", recs[0].Description)
			}
			if strings.Contains(recs[0].Description, "{{") {
				t.Errorf("description = %q still carries an unrendered reference", recs[0].Description)
			}
		})
	}
}

// TestRecommendationFigures_KindOutsideTheClosedSetIsUndecidable is the QuantifierAll
// precedent, applied here: a missing kind is not a gap in coverage, it is a false positive
// waiting for the model to approximate something into it. A recommendation reaching for a
// step-based kind has no step, so the honest answer is that nothing was checked.
func TestRecommendationFigures_KindOutsideTheClosedSetIsUndecidable(t *testing.T) {
	ins := bandInsight()
	for _, kind := range []string{models.FigureCell, models.FigureCount, models.FigureRatio, models.FigureDiff, "", "share"} {
		recs := []models.Recommendation{{
			Figures: []models.Figure{{
				ID: "f1", Value: 52134, Kind: kind,
				Refs: []models.FigureRef{ref(ins.ID, "f1")},
			}},
		}}
		attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
		v := recs[0].FigureVerdicts[0]
		if v.Status != models.FigureUndecidable {
			t.Errorf("kind %q: status = %q, want undecidable", kind, v.Status)
		}
		if v.Status == models.FigureFails {
			t.Errorf("kind %q was refuted; the evaluator's own coverage must never be reported as the document's error", kind)
		}
	}
}

// TestRecommendationFigures_RefNamingSeveralFiguresIsUndecidable: a restatement restates
// one figure. Several is a declaration that does not say what it means, and guessing
// (silently summing, silently taking the first) is how a number ends up attached to the
// wrong label.
func TestRecommendationFigures_RefNamingSeveralFiguresIsUndecidable(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Figures: []models.Figure{{
			ID: "f1", Value: 52134, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f1"), ref(ins.ID, "f2")},
		}},
	}}
	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	if s := recs[0].FigureVerdicts[0].Status; s != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable", s)
	}
}

// TestRecommendationFigures_DerivedRecordsAreClearedBeforeUse mirrors the insight pass.
// Having just been asked for `figures`, volunteering the sibling field is an obvious thing
// for a model to do, and a recommendation that declared nothing is exactly where a
// volunteered verdict would survive unnoticed.
func TestRecommendationFigures_DerivedRecordsAreClearedBeforeUse(t *testing.T) {
	recs := []models.Recommendation{{
		Title:             "No figures here",
		FigureVerdicts:    []models.FigureVerdict{{ID: "f1", Status: models.FigureHolds}},
		FigureCorrections: []models.FigureCorrection{{ID: "f1", From: 1, To: 2}},
		FigureTemplate:    &models.RecommendationFigureTemplate{Title: "authored by the model"},
	}}
	attachRecommendationFigureVerdicts(recs, nil)
	if recs[0].FigureVerdicts != nil || recs[0].FigureCorrections != nil || recs[0].FigureTemplate != nil {
		t.Errorf("a model-authored figure record survived: verdicts=%v corrections=%v template=%v",
			recs[0].FigureVerdicts, recs[0].FigureCorrections, recs[0].FigureTemplate)
	}
}

// TestRecommendationFigures_NotationIsNotDoubledAtEitherEnd proves the renderer is shared
// rather than reimplemented. The suffix end shipped "24.66%%" in one run and the prefix end
// shipped "(~~$151,417)" in the next; a second copy of the rule in a second file is how
// that happens a third time.
func TestRecommendationFigures_NotationIsNotDoubledAtEitherEnd(t *testing.T) {
	ins := models.Insight{
		ID:             "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
		Figures:        []models.Figure{{ID: "f1", Value: 24.66}, {ID: "f2", Value: 151417}},
		FigureVerdicts: []models.FigureVerdict{{ID: "f1", Status: models.FigureHolds}, {ID: "f2", Status: models.FigureHolds}},
	}
	recs := []models.Recommendation{{
		Title:       "Share is {{f1}}% of revenue",
		Description: "Average order value is flat (~{{f2}})",
		Figures: []models.Figure{
			{ID: "f1", Unit: models.UnitPercent, Decimals: 2, Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(ins.ID, "f1")}},
			{ID: "f2", Unit: models.UnitCurrency, Approx: true, Kind: models.FigureRefKind, Refs: []models.FigureRef{ref(ins.ID, "f2")}},
		},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if strings.Contains(recs[0].Title, "%%") {
		t.Errorf("title doubled the percent sign: %q", recs[0].Title)
	}
	if strings.Contains(recs[0].Description, "~~") {
		t.Errorf("description doubled the approximation marker: %q", recs[0].Description)
	}
}

// TestRecommendationFigures_ProjectionsAreNotCountedAsUndeclaredNumbers is the measured
// exclusion. Half the numerals a recommendation writes that are not restatements are the
// model's own estimates -- "+1,500 first-time buyers", "a conservative 3% conversion" --
// and expected_impact is where the schema already puts one. Counting those as undeclared
// would flag the one place a typed number is correct.
func TestRecommendationFigures_ProjectionsAreNotCountedAsUndeclaredNumbers(t *testing.T) {
	tpl := models.RecommendationFigureTemplate{
		Title:             "Activate the dormant accounts",
		Description:       "The accounts have never ordered.",
		Actions:           []string{"Send a first-order offer"},
		ImpactImprovement: "+1,500 first-time buyers",
		ImpactReasoning:   "A conservative 3% conversion on those accounts yields ~1,500 orders.",
	}
	if bare := bareRecommendationNumeralFields(tpl); len(bare) != 0 {
		t.Errorf("projection fields were counted as undeclared numbers: %v", bare)
	}

	// A typed number in the prose fields is still reported, because that is a
	// measurement the model chose not to reference.
	tpl.Title = "Activate the 50,004 dormant accounts"
	if bare := bareRecommendationNumeralFields(tpl); len(bare) != 1 || bare[0] != "title" {
		t.Errorf("bare numeral fields = %v, want [title]", bare)
	}
}

// TestRecommendationSchema_DescribesEveryArrayTheContractAsksFor is the recommendation half
// of the insight test of the same shape, and it closes the same direction: the lockstep
// test walks schema properties and asserts each exists on the struct, which cannot catch a
// key the prompt asks for in prose and the schema never mentions.
func TestRecommendationSchema_DescribesEveryArrayTheContractAsksFor(t *testing.T) {
	schema := recommendationResponseSchema()
	props := schema["properties"].(map[string]interface{})
	items := props["recommendations"].(map[string]interface{})["items"].(map[string]interface{})
	recProps := items["properties"].(map[string]interface{})

	if !strings.Contains(recommendationFigureContract, "figures") {
		t.Fatal(`the recommendation contract no longer mentions "figures"; this test's premise is stale`)
	}
	figures, ok := recProps["figures"]
	if !ok {
		t.Fatal("the recommendation prompt asks the model for \"figures\" but the response schema does not describe it, " +
			"so the model is asked in prose and unguided in structure")
	}
	// And the refs inside it, which is the only field that carries the reference.
	figProps := figures.(map[string]interface{})["items"].(map[string]interface{})["properties"].(map[string]interface{})
	if _, ok := figProps["refs"]; !ok {
		t.Error("the figures schema does not describe `refs`, which is the whole declaration")
	}
	if _, ok := figProps["value"]; ok {
		t.Error("the schema offers the model a `value` to fill; the number comes from the reference, " +
			"and a value field is one the model would be inventing")
	}
}

// TestInsightsForRecommenderPrompt_KeepsFiguresAndDropsTheirAuditTrail is the other half
// of the payload discipline TestInsightsForRecommenderPrompt_DropsTheAuditTrail enforces.
//
// Figures have to stay: the recommender references them by id and cannot do that if they
// are not in the prompt. The three records derived from them have to go, and for the same
// reason Repair does -- a verdict carries the original refuted value in `claimed` and the
// original rendered text in `display`, so a number this pipeline corrected is otherwise
// still in the prompt for the recommender to build on.
func TestInsightsForRecommenderPrompt_KeepsFiguresAndDropsTheirAuditTrail(t *testing.T) {
	in := []models.Insight{{
		ID:      "i1",
		Name:    "All 99,996 buyers are spread across five tiers",
		Figures: []models.Figure{{ID: "f1", Value: 99996, Unit: models.UnitCount, Step: 3, Kind: models.FigureSum, Column: "buyers"}},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f1", Display: "100,000", Status: models.FigureHolds, Claimed: 100000, Evaluated: 99996},
		},
		FigureCorrections: []models.FigureCorrection{{ID: "f1", From: 100000, To: 99996, Text: "100,000 -> 99,996"}},
		FigureTemplate:    &models.FigureTemplate{Name: "All {{f1}} buyers are spread across five tiers"},
	}}

	out := insightsForRecommenderPrompt(in)

	if len(out[0].Figures) != 1 || out[0].Figures[0].ID != "f1" {
		t.Fatalf("figures did not survive: %+v -- the recommender cannot reference what it cannot see", out[0].Figures)
	}
	if out[0].FigureVerdicts != nil || out[0].FigureCorrections != nil || out[0].FigureTemplate != nil {
		t.Errorf("the derived figure records survived: verdicts=%+v corrections=%+v template=%+v",
			out[0].FigureVerdicts, out[0].FigureCorrections, out[0].FigureTemplate)
	}
	// The corrected-away value must not be reachable from the payload at all.
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "100000") || strings.Contains(string(blob), "100,000") {
		t.Errorf("the refuted value is still in the recommender payload:\n%s", blob)
	}
	// And the originals are untouched, because they are what gets stored.
	if in[0].FigureVerdicts == nil || in[0].FigureTemplate == nil {
		t.Error("the stored insight's own audit trail was mutated")
	}
}

// TestRecommendationsPrompt_CarriesTheFigureContract: appended in code rather than added to
// the pack templates, for the reason the analysis contracts are -- a pack file can be
// edited and a custom template skips pack content, so a contract living only in templates
// is one some runs do not have.
func TestRecommendationsPrompt_CarriesTheFigureContract(t *testing.T) {
	o := &Orchestrator{}
	got := o.buildRecommendationsPrompt("BASE", "tpl", "summary", "[]", "ds")
	if !strings.Contains(got, recommendationFigureContract) {
		t.Error("buildRecommendationsPrompt did not append the recommendation figure contract")
	}
}
