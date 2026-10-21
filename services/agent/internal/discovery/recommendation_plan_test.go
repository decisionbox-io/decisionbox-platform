package discovery

import (
	"strings"
	"testing"

	"github.com/decisionbox-io/decisionbox/services/agent/internal/models"
)

const validPlanEnvelope = `{"plan":[
	{"title":"Recover high-value carts","category":"conversion","priority":1,"related_insight_ids":["i-1","i-2"]},
	{"title":"Reactivate lapsed buyers","category":"retention","priority":2,"related_insight_ids":["i-2"]}
]}`

func TestParseRecommendationPlan_Envelope(t *testing.T) {
	items, dropped, err := parseRecommendationPlan(validPlanEnvelope)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(items) != 2 || dropped != 0 {
		t.Fatalf("got %d items, %d dropped; want 2, 0", len(items), dropped)
	}
	if items[0].Title != "Recover high-value carts" || items[0].Priority != 1 {
		t.Errorf("first entry decoded wrong: %+v", items[0])
	}
	if len(items[0].RelatedInsightIDs) != 2 {
		t.Errorf("first entry kept %d ids, want 2", len(items[0].RelatedInsightIDs))
	}
}

func TestParseRecommendationPlan_BareArray(t *testing.T) {
	// Same tolerance the recommendation parser has, for the same reason: some
	// models answer with the array rather than the envelope.
	const arr = `[{"title":"A","related_insight_ids":["i-1"]},{"title":"B"}]`
	items, dropped, err := parseRecommendationPlan(arr)
	if err != nil {
		t.Fatalf("err = %v, want nil (a bare array must be accepted)", err)
	}
	if len(items) != 2 || dropped != 0 {
		t.Fatalf("got %d items, %d dropped; want 2, 0", len(items), dropped)
	}
}

func TestParseRecommendationPlan_CapitalizedKey(t *testing.T) {
	items, _, err := parseRecommendationPlan(`{"Plan":[{"title":"A"}]}`)
	if err != nil {
		t.Fatalf("err = %v, want nil for a capitalized key", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
}

func TestParseRecommendationPlan_DropsUnexpandableEntries(t *testing.T) {
	// A null entry, an entry whose fields are type-wrong, and an untitled
	// entry are each unexpandable — the title is what the expansion call is
	// asked to write. Each is dropped and counted; the rest of the plan lives.
	const mixed = `{"plan":[
		{"title":"Keep me","related_insight_ids":["i-1"]},
		null,
		{"title":"Bad","related_insight_ids":"not an array"},
		{"title":"   "},
		{"category":"conversion"}
	]}`
	items, dropped, err := parseRecommendationPlan(mixed)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(items) != 1 || items[0].Title != "Keep me" {
		t.Fatalf("kept %+v, want only the titled, well-formed entry", items)
	}
	if dropped != 4 {
		t.Errorf("dropped = %d, want 4", dropped)
	}
}

func TestParseRecommendationPlan_MissingKeyIsError(t *testing.T) {
	// A response that answered something else must be an error, not an empty
	// plan — an empty plan would never be re-asked for. In particular, the
	// full recommendation document is not a plan: the phase has to fall back
	// deliberately rather than half-accept it.
	for _, in := range []string{
		`{"recommendations":[{"title":"a"}]}`,
		`{"items":[{"title":"a"}]}`,
		`{}`,
		`{"plan":null}`,
	} {
		if _, _, err := parseRecommendationPlan(in); err == nil {
			t.Errorf("parseRecommendationPlan(%s): want an error, got nil", in)
		}
	}
}

func TestParseRecommendationPlan_NoJSONIsError(t *testing.T) {
	if _, _, err := parseRecommendationPlan("I have decided on four recommendations."); err == nil {
		t.Error("prose with no JSON value must be an error")
	}
}

func TestParseRecommendationPlan_EmptyPlanIsNotAnError(t *testing.T) {
	// The model is allowed to decide the insights support nothing actionable.
	items, dropped, err := parseRecommendationPlan(`{"plan":[]}`)
	if err != nil {
		t.Fatalf("err = %v, want nil for a legitimately empty plan", err)
	}
	if len(items) != 0 || dropped != 0 {
		t.Fatalf("got %d items, %d dropped; want 0, 0", len(items), dropped)
	}
}

func TestRecommendationPlanSuffix_AsksForThePlanAndNothingElse(t *testing.T) {
	s := recommendationPlanSuffix(7)
	for _, want := range []string{`{"plan":[`, "At most 7 entries", "related_insight_ids"} {
		if !strings.Contains(s, want) {
			t.Errorf("plan suffix is missing %q", want)
		}
	}
	// The prose fields belong to the expansion calls; the plan call must be
	// told not to spend its budget on them, which is the whole point of the
	// pass existing.
	for _, field := range []string{"`description`", "`actions`", "`expected_impact`", "`figures`"} {
		if !strings.Contains(s, field) {
			t.Errorf("plan suffix does not tell the model to leave out %s", field)
		}
	}
	if !strings.Contains(s, "Ignore the output format described above") {
		t.Error("plan suffix must override the template's output format")
	}
}

func TestRecommendationExpandSuffix_NamesTheTargetAndRestatesThePlan(t *testing.T) {
	plan := []recommendationPlanItem{
		{Title: "First thing", Category: "conversion", Priority: 1, RelatedInsightIDs: []string{"i-1"}},
		{Title: "Second thing", Category: "retention", Priority: 3, RelatedInsightIDs: []string{"i-2", "i-3"}},
	}
	s := recommendationExpandSuffix(plan, 1)

	// Every planned title is restated, so the entry being written knows the
	// set it belongs to and does not re-make another entry's argument.
	for _, item := range plan {
		if !strings.Contains(s, item.Title) {
			t.Errorf("expansion suffix does not restate %q", item.Title)
		}
	}
	if !strings.Contains(s, "Write entry 2") || !strings.Contains(s, `"Second thing"`) {
		t.Error("expansion suffix must name which entry this response writes")
	}
	if !strings.Contains(s, "<- write this one") {
		t.Error("expansion suffix must mark the target inside the restated plan")
	}
	// The planned ids are the citation the platform resolves against, so they
	// are restated verbatim rather than left to be remembered.
	for _, id := range plan[1].RelatedInsightIDs {
		if !strings.Contains(s, id) {
			t.Errorf("expansion suffix does not restate planned id %q", id)
		}
	}
	if strings.Contains(s, "i-1,") {
		t.Error("expansion suffix must not hand the target the other entry's ids")
	}
	if !strings.Contains(s, `{"recommendations":[`) {
		t.Error("expansion suffix must pin the recommendation envelope")
	}
}

func TestRecommendationTerseSuffix_AsksForShorterNotBetterFormed(t *testing.T) {
	s := recommendationTerseSuffix("the provider stopped at the output limit")
	if !strings.Contains(s, "shorter") {
		t.Error("the truncation retry must ask for a shorter answer")
	}
	if !strings.Contains(s, "the provider stopped at the output limit") {
		t.Error("the truncation retry must name the reason it is re-asking")
	}
}

func TestShrinkPlanItems(t *testing.T) {
	cases := []struct{ in, want int }{
		{20, 10},
		{10, 5},
		{8, 4},
		{6, 3},
		{4, 3}, // halving would go under the floor
		{3, 3}, // already at the floor
		{1, 1}, // never grows a bound that is already smaller than the floor
		{0, 0},
	}
	for _, c := range cases {
		if got := shrinkPlanItems(c.in); got != c.want {
			t.Errorf("shrinkPlanItems(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestApplyPlanToRecommendation_TakesTheCitationWhenTheExpansionHasNone(t *testing.T) {
	rec := models.Recommendation{Title: "X"}
	applyPlanToRecommendation(&rec, recommendationPlanItem{
		Category:          "conversion",
		Priority:          2,
		RelatedInsightIDs: []string{"i-1", "i-2"},
	})
	if len(rec.RelatedInsightIDs) != 2 {
		t.Fatalf("ids = %v, want the plan's two", rec.RelatedInsightIDs)
	}
	if rec.Category != "conversion" || rec.Priority != 2 {
		t.Errorf("category/priority not filled from the plan: %+v", rec)
	}
}

func TestApplyPlanToRecommendation_KeepsASubsetTheExpansionChose(t *testing.T) {
	// Narrowing the citation while writing is a judgement, not a mistake.
	rec := models.Recommendation{Title: "X", RelatedInsightIDs: []string{"i-2"}, Category: "retention", Priority: 4}
	applyPlanToRecommendation(&rec, recommendationPlanItem{
		Category:          "conversion",
		Priority:          1,
		RelatedInsightIDs: []string{"i-1", "i-2"},
	})
	if len(rec.RelatedInsightIDs) != 1 || rec.RelatedInsightIDs[0] != "i-2" {
		t.Errorf("ids = %v, want the expansion's subset kept", rec.RelatedInsightIDs)
	}
	if rec.Category != "retention" || rec.Priority != 4 {
		t.Errorf("the expansion's own category/priority must not be overruled: %+v", rec)
	}
}

func TestApplyPlanToRecommendation_ReplacesCitationsThatAreNoneOfThePlanned(t *testing.T) {
	// The small-model failure this guards: ids that are not ids at all. The
	// plan already named the insights this entry was planned from, so they are
	// a citation the run can stand behind.
	rec := models.Recommendation{Title: "X", RelatedInsightIDs: []string{"conversion:critical:some-slug"}}
	applyPlanToRecommendation(&rec, recommendationPlanItem{RelatedInsightIDs: []string{"i-1"}})
	if len(rec.RelatedInsightIDs) != 1 || rec.RelatedInsightIDs[0] != "i-1" {
		t.Errorf("ids = %v, want the plan's id", rec.RelatedInsightIDs)
	}
}

func TestApplyPlanToRecommendation_DoesNotInventACitation(t *testing.T) {
	// A plan entry with no ids cannot supply one; the downstream citation
	// recovery is what handles that, and it must still see the empty slice.
	rec := models.Recommendation{Title: "X"}
	applyPlanToRecommendation(&rec, recommendationPlanItem{Title: "X"})
	if len(rec.RelatedInsightIDs) != 0 {
		t.Errorf("ids = %v, want none", rec.RelatedInsightIDs)
	}
}

func TestCitesAnyOf(t *testing.T) {
	cases := []struct {
		name      string
		got, want []string
		expect    bool
	}{
		{"one of two", []string{"i-2"}, []string{"i-1", "i-2"}, true},
		{"none", []string{"i-9"}, []string{"i-1"}, false},
		{"empty got", nil, []string{"i-1"}, false},
		{"empty want", []string{"i-1"}, nil, false},
		{"padded", []string{" i-1 "}, []string{"i-1"}, true},
		{"blank want entries", []string{""}, []string{"", "i-1"}, false},
	}
	for _, c := range cases {
		if got := citesAnyOf(c.got, c.want); got != c.expect {
			t.Errorf("%s: citesAnyOf(%v, %v) = %v, want %v", c.name, c.got, c.want, got, c.expect)
		}
	}
}

func TestRecommendationPhaseTokens_CountsTheFanOut(t *testing.T) {
	// The regression this pins: the phase sends the insight context once per
	// call, and an estimate that counts it once is wrong by the fan-out —
	// increasingly so for a richer prompt.
	const contextTokens = 10_000

	in, out := recommendationPhaseTokens(contextTokens)
	if in <= contextTokens {
		t.Fatalf("input estimate = %d, want more than the %d a single call would send", in, contextTokens)
	}
	if want := contextTokens * (1 + estimatedRecommendationsPerRun); in != want {
		t.Errorf("input estimate = %d, want %d (the plan call plus one per recommendation)", in, want)
	}
	if out <= 0 {
		t.Errorf("output estimate = %d, want a positive budget", out)
	}
}

func TestRecommendationPhaseTokens_FollowsTheFanOutBoundDown(t *testing.T) {
	// A lowered bound is a lowered estimate: the operator capped the number of
	// calls, so the preview should say so.
	const contextTokens = 10_000
	t.Setenv(recommendationPlanMaxItemsEnv, "2")

	in, out := recommendationPhaseTokens(contextTokens)
	if want := contextTokens * 3; in != want {
		t.Errorf("input estimate = %d, want %d (plan + 2 expansions)", in, want)
	}

	t.Setenv(recommendationPlanMaxItemsEnv, "1")
	smaller, smallerOut := recommendationPhaseTokens(contextTokens)
	if smaller >= in || smallerOut >= out {
		t.Errorf("a smaller bound did not lower the estimate: %d/%d then %d/%d", in, out, smaller, smallerOut)
	}
}

func TestRecommendationPhaseTokens_RaisedBoundDoesNotInflateTheEstimate(t *testing.T) {
	// The bound exists to cap cost, not to predict it. Raising it does not
	// mean the packs will suddenly ask for more.
	const contextTokens = 10_000
	base, baseOut := recommendationPhaseTokens(contextTokens)

	t.Setenv(recommendationPlanMaxItemsEnv, "500")
	raised, raisedOut := recommendationPhaseTokens(contextTokens)
	if raised != base || raisedOut != baseOut {
		t.Errorf("a raised bound changed the estimate: %d/%d then %d/%d", base, baseOut, raised, raisedOut)
	}
}
