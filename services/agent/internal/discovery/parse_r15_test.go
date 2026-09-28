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
func TestParseReflection_ExplicitlyEmptyObjectThenRealOneIsRetried(t *testing.T) {
	const in = `{"coverage_summary":"","covered_tables":[],"covered_catalog_items":[],"covered_areas":[],"prior_status_updates":[],"learnings":[],"task_status_updates":[],"next_tasks":[],"domain_pack_deltas":[],"convergence_note":""}
{"coverage_summary":"orders covered","next_tasks":[{"title":"Review orders","text":"Look at margin outliers"}]}`
	got, err := parseReflection(in)
	if err == nil {
		t.Fatalf("err = nil (%+v), want an error: every field spelled out empty is still empty", got)
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

func TestParseRecommendations_EmptyShellIsDroppedButATitlelessBodyIsKept(t *testing.T) {
	// Same narrow rule as insights: nothing to show is dropped, a body without a title
	// is kept.
	const in = `{"recommendations":[
		{"priority":"high","segment_size":10},
		{"description":"Re-approve the bulk discount tier before renewal.","priority":"high"},
		{"title":"Real","priority":"low"}
	]}`
	recs, dropped, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 2 {
		t.Fatalf("got %d recommendations (%+v), want 2", len(recs), recs)
	}
	if dropped != 1 {
		t.Errorf("dropped = %d, want 1", dropped)
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
