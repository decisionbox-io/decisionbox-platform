package discovery

import "testing"

// What each parser drops, and what it refuses outright.
//
// A null array element, an item with no title, a question with no question, a stringified
// null, more values than the cap: every one of these arrived in a real model response.
// Dropping the unusable element while keeping the rest is what lets a partly malformed
// answer still carry its findings, and refusing the whole response is reserved for input
// that carries none.

// The unwrap runs after the null check, so a quoted "null" slipped past it and
// unmarshalled into a nil slice with no error -- a malformed response becoming a
// successful empty area. Before the unwrap existed, the string failed as "not an array".
func TestParseInsights_StringifiedNullStillFails(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":"null"}`
	insights, _, err := o.parseInsights(in, "revenue")
	if err == nil {
		t.Fatalf("err = nil with %d insights, want an error", len(insights))
	}
}

// A null array element decodes into a zero-value Insight without error, so it shipped
// as a finding with no name. Reachable unquoted before this commit and through the
// unwrap after it; either way an empty shell is not a finding.
func TestParseInsights_NullArrayElementIsDropped(t *testing.T) {
	o := &Orchestrator{}
	for name, in := range map[string]string{
		"quoted":   `{"insights":"[null]"}`,
		"unquoted": `{"insights":[null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			insights, dropped, err := o.parseInsights(in, "revenue")
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			if len(insights) != 0 {
				t.Fatalf("got %d insights (%+v), want 0: a null element is not a finding", len(insights), insights)
			}
			if dropped != 1 {
				t.Errorf("dropped = %d, want 1", dropped)
			}
		})
	}
}

func TestParseInsights_TitlelessInsightIsDropped(t *testing.T) {
	// An insight with no name is dropped, and so is one with no name but a full body.
	//
	// This reverses an earlier call in this branch. The first version kept a bodied
	// insight with no title, reasoning that a model asked to write the body before the
	// title -- which is what the Fix T reordering asks -- might omit the title and still
	// have produced the analysis. Review found that concrete: the insights page
	// deduplicates on `${analysis_area}:${insight.name}`, so every titleless insight in
	// an area collapses into one, and renders `{insight.name}` as the visible label and
	// the search seed title, so what survives is a blank row. A finding that cannot be
	// read or linked is not shipped by keeping it.
	//
	// Nothing observed emits one: every insight in 12 replays and both adjudicated runs
	// carried a name. Synthesising a title from the body would keep the content and is
	// recorded as a follow-up, but the parser is the wrong place to author prose.
	o := &Orchestrator{}
	const in = `{"insights":[
		{"severity":"high","affected_count":10},
		{"description":"Returns are concentrated in one region, at 14% against 6% elsewhere.","severity":"high"},
		{"name":"Real","severity":"low"}
	]}`
	insights, dropped, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || insights[0].Name != "Real" {
		t.Fatalf("got %d insights (%+v), want just the named one", len(insights), insights)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

// Insights got the null-element guard; recommendations did not. A null element
// unmarshals into a zero-value Recommendation without error, and generateRecommendations
// accepts the batch because it is non-empty -- so a blank recommendation is assigned an
// ID and reaches citation recovery.
func TestParseRecommendations_NullElementIsDropped(t *testing.T) {
	const in = `{"recommendations":[null,{"title":"Real action","priority":"high"}]}`
	recs, dropped, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 1 || recs[0].Title != "Real action" {
		t.Fatalf("got %d recommendations (%+v), want just the real one", len(recs), recs)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}

func TestParseRecommendations_TitlelessRecommendationIsDropped(t *testing.T) {
	// Same reversal as insights, and review found the breakage here first: the run page
	// renders the recommendation link from `rec.title`, and the recommendations page
	// deduplicates by title, so every titleless recommendation collapses under the same
	// empty-string key.
	const in = `{"recommendations":[
		{"priority":"high","segment_size":10},
		{"description":"Re-approve the bulk discount tier before renewal.","priority":"high"},
		{"title":"Real","priority":"low"}
	]}`
	recs, dropped, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 1 || recs[0].Title != "Real" {
		t.Fatalf("got %d recommendations (%+v), want just the titled one", len(recs), recs)
	}
	if dropped != 2 {
		t.Errorf("dropped = %d, want 2", dropped)
	}
}

func TestParseQuestions_NullElementIsDropped(t *testing.T) {
	const in = `{"questions":[null,{"question":"Which region drives returns?"}]}`
	qs, _, err := parseQuestions(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(qs) != 1 || qs[0].Question != "Which region drives returns?" {
		t.Fatalf("got %d questions (%+v), want just the real one", len(qs), qs)
	}
}

func TestParseQuestions_QuestionlessElementIsDropped(t *testing.T) {
	// A question with no question text is nothing to ask.
	const in = `{"questions":[{"rationale":"because"},{"question":"Real?"}]}`
	qs, _, err := parseQuestions(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(qs) != 1 || qs[0].Question != "Real?" {
		t.Fatalf("got %d questions (%+v), want just the real one", len(qs), qs)
	}
}

// The title requirement is for first write. A repair rewrite legitimately omits the
// name, because mergeRepairedInsight keeps the original's -- so dropping it there failed
// a repair round that had actually produced a corrected sentence.
func TestParseInsightsStrict_KeepsARewriteWithNoName(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":[{
		"description":"Tables is one of two loss-making sub-categories.",
		"severity":"high",
		"source_steps":[4],
		"quantifier_claims":[{"claim":"Tables is one of two loss-making sub-categories.","kind":"cardinality","step":4,"filter":"profit < 0","count":2}]
	}]}`
	insights, dropped, err := o.parseInsightsStrict(in, "profitability")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 {
		t.Fatalf("got %d insights, %d dropped; want the rewrite kept -- the repair merge supplies the name", len(insights), dropped)
	}
	if len(insights[0].QuantifierClaims) != 1 {
		t.Errorf("declaration lost: %+v", insights[0])
	}
}

func TestParseInsights_StillRequiresANameAtFirstWrite(t *testing.T) {
	// And the first-write path still drops it, so the two paths are genuinely different
	// rather than the requirement having been deleted.
	o := &Orchestrator{}
	const in = `{"insights":[{"description":"A body with no title.","severity":"high"}]}`
	insights, dropped, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 0 || dropped != 1 {
		t.Fatalf("got %d insights, %d dropped; want 0, 1", len(insights), dropped)
	}
}

// A response carrying more top-level values than the cap is pathological, and silently
// reading the first of them would hide whatever is past the cap.
func TestParseInsights_MoreValuesThanTheCapIsAnError(t *testing.T) {
	o := &Orchestrator{}
	in := ""
	for i := 0; i < maxResponseValues; i++ {
		in += "[]\n"
	}
	in += `[{"name":"Actual finding","severity":"high"}]`
	insights, _, err := o.parseInsights(in, "revenue")
	if err == nil {
		t.Fatalf("err = nil with %d insights, want an error: the response ran past the value cap", len(insights))
	}
}

// Two spellings of the same key in one object made the result depend on map iteration
// order. Whatever is chosen, it must be the same every time.
func TestEnvelopeItems_CaseVariantKeysAreDeterministic(t *testing.T) {
	const in = `{"Insights":[],"insights":[{"name":"Actual finding","severity":"high"}]}`
	first, err := envelopeItems([]byte(in), "insights")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	for i := 0; i < 200; i++ {
		got, err := envelopeItems([]byte(in), "insights")
		if err != nil {
			t.Fatalf("err = %v on iteration %d", err, i)
		}
		if len(got) != len(first) {
			t.Fatalf("iteration %d returned %d items, first call returned %d: key choice depends on map order", i, len(got), len(first))
		}
	}
	// And the exact spelling is the one that wins, so the choice is stated rather than
	// incidental. Here that is the lower-case key, which holds the real array.
	if len(first) != 1 {
		t.Errorf("got %d items, want 1 -- the exactly-matching key's value", len(first))
	}
}
