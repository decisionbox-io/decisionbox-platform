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
	// And in both cases the model's own stated number must survive. Overwriting it with
	// a value Go declined to vouch for is not a correction, and the cited insight is
	// already showing that number to the same reader anyway.
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
			if got := recs[0].Figures[0].Value; got != 100000 {
				t.Errorf("figure value = %v, want the model's own 100000 left standing -- Go must not "+
					"overwrite a stated number with one it will not vouch for", got)
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
	// The derived records cannot survive a field the projection does not declare,
	// so what this asserts is that none of them was added back to it. Checked on the
	// rendered payload rather than on the struct, because that is what reaches the
	// model.
	blob, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"evidence_figures":`, `"evidence_figure_corrections":`, `"evidence_figure_template":`} {
		if strings.Contains(string(blob), key) {
			t.Errorf("a derived figure record is in the recommender payload (%s):\n%s", key, blob)
		}
	}
	// The corrected-away value must not be reachable from the payload at all.
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

// TestInsightSchema_FigureEnumsMatchTheClosedSets pins the schema's prose enumerations to
// the constants the evaluator actually reads.
//
// It exists because they drifted, in the direction that matters most. `days` was removed
// from models.Figure's units after the first live run shipped "180.1 days days", and the
// schema went on advertising it -- so the contract told the model in prose never to use a
// word while the schema listed one by name, and the schema is the stronger signal. A field
// description is not a free-text comment; for a decode-constrained provider it is the spec.
//
// Driven off the constants rather than a hand-written list, so adding or removing a unit,
// scale or kind fails here until the schema is updated too.
func TestInsightSchema_FigureEnumsMatchTheClosedSets(t *testing.T) {
	figProps := insightSchemaProperties(t)["figures"].(map[string]interface{})["items"].(map[string]interface{})["properties"].(map[string]interface{})

	desc := func(field string) string {
		return figProps[field].(map[string]interface{})["description"].(string)
	}

	// Every member of the closed set must be named, and nothing outside it.
	units := []string{models.UnitCount, models.UnitCurrency, models.UnitPercent, models.UnitMultiple, models.UnitPlain}
	unitDesc := desc("unit")
	for _, u := range units {
		if !strings.Contains(unitDesc, `"`+u+`"`) {
			t.Errorf("the schema's unit description omits %q, so the model is not told it is available: %s", u, unitDesc)
		}
	}
	// The one that drifted. A word unit cannot be offered, however it is phrased.
	if strings.Contains(unitDesc, `"days"`) {
		t.Errorf("the schema offers a days unit; a unit is notation and a word is prose, which is why the code has no such unit: %s", unitDesc)
	}

	kinds := []string{models.FigureCell, models.FigureSum, models.FigureCount,
		models.FigureRatio, models.FigureExcess, models.FigureDiff}
	kindDesc := desc("kind")
	for _, k := range kinds {
		if !strings.Contains(kindDesc, `"`+k+`"`) {
			t.Errorf("the schema's kind description omits %q: %s", k, kindDesc)
		}
	}
	// A recommendation-only kind must not be offered to an insight: there is nothing in
	// an insight's figure to hold a reference, so a `ref` here would be undecidable.
	if strings.Contains(kindDesc, `"`+models.FigureRefKind+`"`) {
		t.Errorf("the schema offers an insight the recommendation-only kind %q: %s", models.FigureRefKind, kindDesc)
	}

	scaleDesc := desc("scale")
	for _, sc := range []string{models.ScaleThousands, models.ScaleMillions, models.ScaleBillions} {
		if !strings.Contains(scaleDesc, `"`+sc+`"`) {
			t.Errorf("the schema's scale description omits %q: %s", sc, scaleDesc)
		}
	}
}

// TestFigureContract_KeepsEveryRuleThatTracesToAMeasuredFailure guards the shortening.
//
// The contract was cut down once and will be again, and each of these clauses is there
// because a run shipped something wrong without it. A trim that removes one should fail
// here and be argued for explicitly rather than pass quietly.
func TestFigureContract_KeepsEveryRuleThatTracesToAMeasuredFailure(t *testing.T) {
	required := map[string]string{
		"never a word":                 `the days unit that shipped "180.1 days days"`,
		"precision you are claiming":   "decimals-as-interval, which is the whole check",
		"does not loosen the check":    "a tilde must not be a way to make a figure unrefutable",
		"even when you are unsure":     "an undeclared figure cannot be corrected",
		"Never type a number":          "the instruction the entire layer rests on",
		"Years are the only exception": "requiring a declaration for a period makes the common case unwriteable",
		"**full** rows":                "the check runs over more rows than the digest showed",
		// The `excess` kind, and the instruction to pick it from what the sentence says.
		// Naming the kinds is not enough: one run had the contract correctly stating that
		// `ratio` is the quotient, the model complied, and the error moved into the prose
		// as "109.6% more" behind a VOUCHED figure whose excess is 9.6%.
		"- `excess`":                  "the kind has to be named or the model cannot declare it",
		"quotient **minus one**":      "what `excess` computes, for a sentence saying one thing exceeds another",
		"Pick the kind from what the": "which of the two a declaration means is only in the sentence",
	}
	for clause, why := range required {
		if !strings.Contains(figureContract, clause) {
			t.Errorf("the figure contract no longer says %q -- %s", clause, why)
		}
	}
	// And the clause added when the first clean run produced three insights with no
	// indicators at all, against four per insight before the contract existed.
	if !strings.Contains(figureContract, "needs no declaration") {
		t.Error("the contract does not say an indicator with no number needs no declaration, " +
			"which is the likeliest reason a model reads \"never type a number\" as \"say nothing\"")
	}
}

// The three cases below all come from the first live replay of this layer. Each one is a
// defect the unit tests above did not reach, because each needed a model that actually
// follows the contract -- and the contract says do not write a value.

// TestRecommendationFigures_NoStatedValueIsComplianceNotACorrection.
//
// The replay declared nineteen figures and stated a value on none of them, which is exactly
// what the contract asks for. Eighteen were then logged as corrected from zero, so
// following the instruction looked identical to getting the number wrong -- in the one
// counter that would be used to measure whether the layer works.
func TestRecommendationFigures_NoStatedValueIsComplianceNotACorrection(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Description: "The band holds {{f1}} customers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f1")},
		}},
	}}

	_, adopted := attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if v := recs[0].FigureVerdicts[0]; v.Status != models.FigureHolds {
		t.Errorf("status = %q (%s), want holds -- an unstated value is the contract being followed", v.Status, v.Reason)
	}
	if len(recs[0].FigureCorrections) != 0 {
		t.Errorf("following the contract was logged as a correction: %v", recs[0].FigureCorrections)
	}
	if adopted != 0 {
		t.Errorf("adopted = %d, want 0 -- filling in a number the model was told not to write is not an adoption", adopted)
	}
	if !strings.Contains(recs[0].Description, "52,134") {
		t.Errorf("description = %q, want the resolved value rendered", recs[0].Description)
	}
}

// TestRecommendationFigures_UnvouchedOperandCarriesItsNumberNotAZero is the sentence the
// replay shipped: "Discounts reduced gross revenue by $11.48B — 0.00% of gross $229.58B".
//
// The reference pointed at an insight figure whose own check had come back refuted. The
// operand was treated as unusable, the model had written no value because the contract told
// it not to, and the absence rendered as zero -- so a fabricated 0.00% went into the prose
// where the true share is around 5%. A typed number would have been right, which makes this
// strictly worse than having no contract at all.
//
// A refuted insight figure still has a value, and the correction gate leaves it visible in
// that insight on purpose. So the restatement carries it, and says undecidable.
func TestRecommendationFigures_UnvouchedOperandCarriesItsNumberNotAZero(t *testing.T) {
	ins := models.Insight{
		ID: "30ee25a4-1111-2222-3333-444444444444",
		Figures: []models.Figure{
			{ID: "f3", Value: 4.9983, Unit: models.UnitPercent, Decimals: 2},
		},
		FigureVerdicts: []models.FigureVerdict{
			{ID: "f3", Status: models.FigureFails, Claimed: 4.9983, Evaluated: 0.049983},
		},
	}
	recs := []models.Recommendation{{
		Description: "Discounts gave up {{f2}} of gross revenue.",
		Figures: []models.Figure{{
			ID: "f2", Unit: models.UnitPercent, Decimals: 2, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref(ins.ID, "f3")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	renderRecommendationFigures(recs)

	if strings.Contains(recs[0].Description, "0.00%") {
		t.Fatalf("a fabricated zero shipped into the prose: %q", recs[0].Description)
	}
	if !strings.Contains(recs[0].Description, "5.00%") {
		t.Errorf("description = %q, want the cited insight's own number (5.00%%)", recs[0].Description)
	}
	v := recs[0].FigureVerdicts[0]
	if v.Status != models.FigureUndecidable {
		t.Errorf("status = %q, want undecidable -- the number is carried, not vouched for", v.Status)
	}
	if !v.Resolved {
		t.Error("resolved = false, but a value was produced; the renderer needs that distinction")
	}
}

// TestRecommendationFigures_UnresolvableAndUnstatedKeepsItsMarker is the other half. When
// there is no number anywhere -- the id resolves to nothing and the model wrote no value --
// the reference stays visible rather than rendering zero. "{{f2}}" tells a reader something
// went wrong; "0.00%" reads like a finding.
func TestRecommendationFigures_UnresolvableAndUnstatedKeepsItsMarker(t *testing.T) {
	ins := bandInsight()
	recs := []models.Recommendation{{
		Description: "Target the {{f1}} buyers.",
		Figures: []models.Figure{{
			ID: "f1", Unit: models.UnitCount, Kind: models.FigureRefKind,
			Refs: []models.FigureRef{ref("no-such-insight", "f1")},
		}},
	}}

	attachRecommendationFigureVerdicts(recs, []models.Insight{ins})
	tally := renderRecommendationFigures(recs)

	if strings.Contains(recs[0].Description, " 0 ") || strings.Contains(recs[0].Description, "the 0 buyers") {
		t.Fatalf("a fabricated zero shipped into the prose: %q", recs[0].Description)
	}
	if !strings.Contains(recs[0].Description, "{{f1}}") {
		t.Errorf("description = %q, want the reference left visible when there is no number behind it", recs[0].Description)
	}
	if tally.unresolved != 1 {
		t.Errorf("tally.unresolved = %d, want 1", tally.unresolved)
	}
}
