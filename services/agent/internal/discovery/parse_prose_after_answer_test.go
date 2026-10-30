package discovery

import "testing"

// Prose after the answer, which must never be mistaken for a second answer.
//
// Each case here broke an earlier attempt to classify trailing text by looking at it: prose
// that opens with a bracket, prose that quotes a JSON example, a citation marker, a fenced
// block, and an explanation that quotes the envelope's own key. The heuristic was removed
// rather than narrowed a sixth time -- see response_values.go -- and these assert the cases
// that broke it against the design that no longer asks the question.

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

// A citation marker and a JSON example inside prose both parse as composite values, so
// the previous test read them as a second answer and rejected the correct empty one.
func TestParseInsights_EmptyEnvelopeThenJSONLookingProseIsAccepted(t *testing.T) {
	o := &Orchestrator{}
	for name, tail := range map[string]string{
		"citation marker": "[1] No session-level data exists in this schema.",
		"json example":    `[{"session_id":"..."}] would be required to answer this, but the schema has no session data.`,
		"object example":  `{"session_id":"..."} is the shape this would need; nothing like it exists here.`,
		"bracketed note":  "[No session-level data is present in this schema.]",
		"number":          "0 session-level rows were available.",
		"bare null":       "null means no insight is supported by this schema.",
	} {
		t.Run(name, func(t *testing.T) {
			insights, _, err := o.parseInsights("{\"insights\": []}\n\n"+tail, "session_behavior")
			if err != nil {
				t.Fatalf("err = %v, want nil: an empty area with an explanation is the correct answer here", err)
			}
			if len(insights) != 0 {
				t.Fatalf("got %d insights, want 0", len(insights))
			}
		})
	}
}

// cleanJSONResponse preferred any later ```json fence over JSON the response already
// started with, so a real answer followed by a quoted example of an empty one returned
// the example.
func TestParseInsights_LeadingJSONBeatsAQuotedExample(t *testing.T) {
	o := &Orchestrator{}
	in := "{\"insights\":[{\"name\":\"Actual finding\",\"severity\":\"high\"}]}\n\n" +
		"For reference, an empty response looks like:\n```json\n{\"insights\":[]}\n```"
	insights, _, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || insights[0].Name != "Actual finding" {
		t.Fatalf("got %d insights (%+v), want the real one, not the quoted example", len(insights), insights)
	}
}

func TestParseInsights_FencedJSONStillWorksWhenProseComesFirst(t *testing.T) {
	// The other half: a fence is still how the answer arrives when the model opens with
	// prose, so the new short-circuit must not break that.
	o := &Orchestrator{}
	in := "Here is the analysis.\n```json\n{\"insights\":[{\"name\":\"Fenced\",\"severity\":\"low\"}]}\n```"
	insights, _, err := o.parseInsights(in, "revenue")
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if len(insights) != 1 || insights[0].Name != "Fenced" {
		t.Fatalf("got %d insights (%+v), want the fenced one", len(insights), insights)
	}
}

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
