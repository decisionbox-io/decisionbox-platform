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

func TestParseInsights_EmptyShellIsDroppedButANamelessBodyIsKept(t *testing.T) {
	// The same hole as the null element, reached by a different route: an object that
	// decodes cleanly but carries neither a name nor a body has nothing to show and is
	// not a finding.
	//
	// Deliberately narrow. Dropping every insight with no `name` would be the obvious
	// rule and would lose real findings: a model that writes the body first -- which is
	// exactly what the Fix T prompt reordering asks it to do -- and then omits or
	// truncates the title has still produced the analysis. Only an insight with nothing
	// at all to show is discarded.
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
	if len(insights) != 2 {
		t.Fatalf("got %d insights (%+v), want 2: the empty shell dropped, the nameless body kept", len(insights), insights)
	}
	if insights[0].Name != "" || insights[0].Description == "" {
		t.Errorf("first kept insight should be the nameless body, got %+v", insights[0])
	}
	if insights[1].Name != "Real" {
		t.Errorf("second kept insight = %q, want Real", insights[1].Name)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
	}
}
