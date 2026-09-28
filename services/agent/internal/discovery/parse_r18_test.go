package discovery

// Round 18. Its first finding is the sixth time the trailing-text heuristic broke the
// case this whole sequence exists to protect -- a correctly empty area with an
// explanation attached -- this time because the explanation quotes the envelope key.
// The heuristic is removed rather than narrowed a sixth time; see response_values.go.

import "testing"

func TestParseInsights_EmptyEnvelopeThenProseQuotingTheKeyIsAccepted(t *testing.T) {
	o := &Orchestrator{}
	for name, tail := range map[string]string{
		"quotes the key":   `The "insights" array is empty because this schema has no session-level data.`,
		"names the schema": `No "session_id" column exists, so the "insights" list is empty.`,
	} {
		t.Run(name, func(t *testing.T) {
			insights, _, err := o.parseInsights("{\"insights\": []}\n\n"+tail, "session_behavior")
			if err != nil {
				t.Fatalf("err = %v, want nil: an explanation may quote the key it is explaining", err)
			}
			if len(insights) != 0 {
				t.Fatalf("got %d insights, want 0", len(insights))
			}
		})
	}
}

func TestParseRecommendations_EmptyEnvelopeThenProseQuotingTheKeyIsAccepted(t *testing.T) {
	const in = `{"recommendations":[]}

The "recommendations" array is empty because no insight carries a sizeable population.`
	recs, _, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(recs) != 0 {
		t.Fatalf("got %d recommendations, want 0", len(recs))
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
