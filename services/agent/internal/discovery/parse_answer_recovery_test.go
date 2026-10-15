package discovery

import "testing"

// Recovering the real answer when a placeholder came first.
//
// A model that emits an empty envelope and then its real answer used to lose the answer
// twice over: first the trailing data made the whole response malformed and the phase was
// re-prompted, then ignoring trailing bytes made the PLACEHOLDER the answer and threw the
// findings away silently. Two of five analysis areas were lost in every replay that way,
// and one lost response held a sound insight explaining that the schema could not support
// the analysis asked for.
//
// So "is there a real answer here" is settled by decoding every value in the response,
// never by reading the text around them.

// A model that emits a placeholder envelope and then its real answer. Before the
// trailing-prose change this failed on the trailing data and was re-prompted; ignoring
// trailing bytes made the placeholder the answer and silently threw the findings away.
//
// Neither is what this does now: the real answer is USED. Retrying was the second-best
// outcome -- it costs a call and the re-prompt is not guaranteed to reproduce what was
// already sitting in the response.
func TestParseInsights_EmptyEnvelopeThenRealOneRecoversTheRealOne(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":[]}
{"insights":[{"name":"Actual finding","severity":"high","affected_count":12}]}`
	insights, _, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || insights[0].Name != "Actual finding" {
		t.Fatalf("got %d insights (%+v), want the real one", len(insights), insights)
	}
}

func TestParseInsights_NonEmptyEnvelopeThenAnotherTakesTheFirst(t *testing.T) {
	// The other half of the rule: the FIRST value that produces a finding wins, so a
	// second envelope behind a usable one is ignored rather than preferred.
	o := &Orchestrator{}
	const in = `{"insights":[{"name":"First","severity":"high"}]}
{"insights":[{"name":"Second","severity":"low"}]}`
	insights, _, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || insights[0].Name != "First" {
		t.Fatalf("got %d insights (%v), want 1 named First", len(insights), insights)
	}
}

func TestParseRecommendations_EmptyEnvelopeThenRealOneRecoversTheRealOne(t *testing.T) {
	const in = `{"recommendations":[]}
{"recommendations":[{"title":"Actual action","priority":"high"}]}`
	recs, _, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 1 || recs[0].Title != "Actual action" {
		t.Fatalf("got %d recommendations (%+v), want the real one", len(recs), recs)
	}
}

func TestParseQuestions_EmptyEnvelopeThenRealOneRecoversTheRealOne(t *testing.T) {
	const in = `{"questions":[]}
{"questions":[{"question":"Which region drives returns?"}]}`
	qs, _, err := parseQuestions(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(qs) != 1 || qs[0].Question != "Which region drives returns?" {
		t.Fatalf("got %d questions (%+v), want the real one", len(qs), qs)
	}
}

func TestParseReflection_EmptyObjectThenRealOneRecoversTheRealOne(t *testing.T) {
	const in = `{}
{"covered_areas":["revenue"]}`
	got, err := parseReflection(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if got == nil || len(got.CoveredAreas) != 1 {
		t.Fatalf("got %+v, want the real reflection", got)
	}
}

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

// A COMPLETE second envelope is still recovered -- that is the part that matters and it
// is not a heuristic, because the value parsed. Only a BROKEN second envelope is missed,
// and that limit is asserted in parse_r15_test.go with the reasoning.
func TestParseInsights_EmptyEnvelopeThenCompleteRealOneRecoversIt(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":[]}
{"insights":[{"name":"Actual finding","severity":"high"}]}`
	insights, _, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || insights[0].Name != "Actual finding" {
		t.Fatalf("got %d insights (%+v), want the real one", len(insights), insights)
	}
}
