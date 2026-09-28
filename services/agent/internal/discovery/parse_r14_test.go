package discovery

// Round 14 review findings. Each asserts a case the trailing-prose change made worse
// than it was, so the fix for it can be proven rather than assumed.

import "testing"

// A model that emits a placeholder envelope and then its real answer. Before the
// change this failed on the trailing data and was re-prompted; ignoring trailing bytes
// made the placeholder the answer and silently threw the findings away. The comment in
// decodeLeadingJSON called resolving to the first value a deliberate consequence -- and
// it is, when that value carries content. An empty first value is not an answer.
func TestParseInsights_EmptyEnvelopeThenRealOneIsRetried(t *testing.T) {
	o := &Orchestrator{}
	const in = `{"insights":[]}
{"insights":[{"name":"Actual finding","severity":"high","affected_count":12}]}`
	insights, _, err := o.parseInsights(in, "revenue")
	if err == nil {
		t.Fatalf("err = nil with %d insights, want an error: an empty envelope followed by a second one must be re-prompted, not shipped as empty", len(insights))
	}
}

func TestParseInsights_NonEmptyEnvelopeThenAnotherTakesTheFirst(t *testing.T) {
	// The deliberate half of the same rule: a first value that carries content is a
	// usable answer, and rejecting it would cost a finding to protect against a shape
	// no model has been seen to emit.
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

func TestParseRecommendations_EmptyEnvelopeThenRealOneIsRetried(t *testing.T) {
	const in = `{"recommendations":[]}
{"recommendations":[{"title":"Actual action","priority":"high"}]}`
	recs, _, err := parseRecommendations(in)
	if err == nil {
		t.Fatalf("err = nil with %d recommendations, want an error", len(recs))
	}
}

func TestParseQuestions_EmptyEnvelopeThenRealOneIsRetried(t *testing.T) {
	const in = `{"questions":[]}
{"questions":[{"question":"Which region drives returns?"}]}`
	qs, _, err := parseQuestions(in)
	if err == nil {
		t.Fatalf("err = nil with %d questions, want an error", len(qs))
	}
}

func TestParseReflection_EmptyObjectThenRealOneIsRetried(t *testing.T) {
	const in = `{}
{"drop_steps":[4,7]}`
	got, err := parseReflection(in)
	if err == nil {
		t.Fatalf("err = nil (%+v), want an error: an empty leading object must not silence a real reflection", got)
	}
}

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

// The trailing-JSON test must not fire on prose that merely starts with a bracket.
// A legitimately empty area is the common case -- TPC-H has no session data, so
// `{"insights":[]}` plus an explanation is the correct answer -- and rejecting it
// re-prompts for nothing.
func TestParseInsights_EmptyEnvelopeThenBracketedProseIsAccepted(t *testing.T) {
	o := &Orchestrator{}
	for name, in := range map[string]string{
		"bracketed note": "{\"insights\": []}\n\n[No session-level data is present in this schema.]",
		"braced note":    "{\"insights\": []}\n\n{No funnel events exist in these tables.}",
		"json example":   "{\"insights\": []}\n\nThe schema would need a shape like [{\"session_id\": \"...\"}] to answer this.",
	} {
		t.Run(name, func(t *testing.T) {
			insights, _, err := o.parseInsights(in, "session_behavior")
			if err != nil {
				t.Fatalf("err = %v, want nil: trailing prose is not a second answer just because it opens with a bracket", err)
			}
			if len(insights) != 0 {
				t.Fatalf("got %d insights, want 0", len(insights))
			}
		})
	}
}
