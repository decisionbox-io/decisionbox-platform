package discovery

// Round 15 review findings. Its first finding -- prose beginning with a bracket read as
// a second JSON value -- was already fixed in 45451ce before the round returned, and
// TestParseInsights_EmptyEnvelopeThenBracketedProseIsAccepted covers it. These are the
// two it found that were still open, plus the same holes in the parsers that were
// missed when insights got the guard.

import "testing"

// reflect.DeepEqual against a zero parsedReflection only catches a literal `{}`. A model
// that spells out every field as an empty array produces something semantically empty
// whose slices are non-nil, so it compared unequal, passed as an answer, and silenced
// the real reflection behind it.
func TestParseReflection_ExplicitlyEmptyObjectThenRealOneRecoversTheRealOne(t *testing.T) {
	const in = `{"coverage_summary":"","covered_tables":[],"covered_catalog_items":[],"covered_areas":[],"prior_status_updates":[],"learnings":[],"task_status_updates":[],"next_tasks":[],"domain_pack_deltas":[],"convergence_note":""}
{"coverage_summary":"orders covered","next_tasks":[{"title":"Review orders","text":"Look at margin outliers"}]}`
	got, err := parseReflection(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == nil || got.CoverageSummary != "orders covered" {
		t.Fatalf("got %+v, want the real reflection: every field spelled out empty is still empty", got)
	}
}

func TestParseReflection_ExplicitlyEmptyAloneIsAccepted(t *testing.T) {
	// The other half: reflecting and finding nothing to change is a legitimate answer
	// when nothing follows it.
	const in = `{"covered_tables":[],"next_tasks":[]}`
	got, err := parseReflection(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("got nil")
	}
}

func TestParseReflection_OneFilledFieldIsNotEmpty(t *testing.T) {
	// A single populated field is an answer, so the emptiness test must not swallow it.
	for name, in := range map[string]string{
		"summary only": `{"coverage_summary":"orders covered","covered_tables":[]}` + "\n" + `{"covered_areas":["revenue"]}`,
		"one table":    `{"covered_tables":["orders"]}` + "\n" + `{"covered_areas":["revenue"]}`,
		"one task":     `{"next_tasks":[{"title":"t","text":"x"}]}` + "\n" + `{"covered_areas":["revenue"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := parseReflection(in)
			if err != nil {
				t.Fatalf("err = %v, want nil: a populated field is an answer and the first value wins", err)
			}
			if got == nil {
				t.Fatal("got nil")
			}
		})
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

// --- round 16 ---

// A KNOWN LIMIT, asserted so it is a decision rather than an accident.
//
// A placeholder followed by a real answer that was cut off mid-value ships as an empty
// area. Catching it needs some test on the unparsed remainder, and every version of that
// test -- five of them -- broke the case this sequence exists to protect: a correctly
// empty area whose explanation happens to look like, or quote, an answer.
//
// The trade is decided on what has been seen. A correctly empty area with an explanation
// was observed 17 times over 12 replays and in every full run. A truncated second answer
// behind an empty first one has never been observed once. A response cut off by a token
// limit is also visible directly in the LLM result's stop reason, which is where that
// check belongs -- not in a parser guessing from text.
func TestParseInsights_EmptyEnvelopeThenTruncatedRealOneShipsEmpty(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":[]}
{"insights":[{"name":"Actual finding","severity":"high"`
	insights, _, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil -- see the note above: the remainder is not inspected", err)
	}
	if len(insights) != 0 {
		t.Fatalf("got %d insights, want 0", len(insights))
	}
}

func TestParseRecommendations_EmptyEnvelopeThenTruncatedRealOneShipsEmpty(t *testing.T) {
	const in = `{"recommendations":[]}
{"recommendations":[{"title":"Actual action"`
	recs, _, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil -- same known limit", err)
	}
	if len(recs) != 0 {
		t.Fatalf("got %d recommendations, want 0", len(recs))
	}
}

// A second JSON value that is a SCALAR is prose, not an answer. An explanation opening
// with a number, a bare null, a bool or a quote parsed as a valid JSON value and got the
// correct empty answer rejected -- the common case for this data, since two of five
// areas are legitimately empty in every run.
func TestParseInsights_EmptyEnvelopeThenScalarLeadingProseIsAccepted(t *testing.T) {
	o := &Orchestrator{}
	for name, tail := range map[string]string{
		"number": "0 session-level rows were available, so no session insights can be produced.",
		"null":   "null means no insight is supported by this schema.",
		"bool":   "true: no funnel table exists in these tables.",
		"quoted": "\"No session data is present.\" is the only honest summary here.",
	} {
		t.Run(name, func(t *testing.T) {
			insights, _, err := o.parseInsights("{\"insights\": []}\n\n"+tail, "session_behavior")
			if err != nil {
				t.Fatalf("err = %v, want nil: a scalar is prose, not a second answer", err)
			}
			if len(insights) != 0 {
				t.Fatalf("got %d insights, want 0", len(insights))
			}
		})
	}
}

// JSON null unmarshals into a non-pointer struct without error, so a bare `null` came
// back as an empty reflection and was accepted instead of retried.
func TestParseReflection_BareNullIsAnError(t *testing.T) {
	for _, in := range []string{"null", "  null  ", "[]", `"nothing to reflect on"`, "42"} {
		got, err := parseReflection(in)
		if err == nil {
			t.Errorf("parseReflection(%q) = %+v, nil; want an error: the contract is an object", in, got)
		}
	}
}
