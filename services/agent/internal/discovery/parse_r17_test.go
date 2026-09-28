package discovery

// Round 17 findings. Two of them were the fourth and fifth time the same
// classification question -- "is this trailing text an answer or prose" -- got a wrong
// answer, which is why the question was abandoned rather than refined again. These
// assert the cases that broke it, against the design that no longer asks it.

import "testing"

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

// A second envelope that is malformed rather than truncated -- a trailing comma -- is
// still an attempt at an answer, and shipping the empty placeholder would lose it.
//
// Recognised by the unparsed remainder naming the envelope key in QUOTED form, which is
// the whole of the test. An unquoted key (`{insights:[...]}`) is therefore missed, and
// deliberately: searching for the bare word would fire on the prose this all exists to
// accept -- the captured conversion response says "no valid conversion funnel insights
// can be produced". Missing an invalid-JSON shape no model has been seen to emit costs
// less than re-prompting every correctly empty area.
func TestParseInsights_EmptyEnvelopeThenMalformedRealOneIsRetried(t *testing.T) {
	o := &Orchestrator{}
	for name, in := range map[string]string{
		"trailing comma": `{"insights":[]}
{"insights":[{"name":"Actual finding","severity":"high",}]}`,
		"truncated": `{"insights":[]}
{"insights":[{"name":"Actual finding","severity":"high"`,
	} {
		t.Run(name, func(t *testing.T) {
			insights, _, err := o.parseInsights(in, "revenue")
			if err == nil {
				t.Fatalf("err = nil with %d insights, want an error: a broken second answer must be re-prompted", len(insights))
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
